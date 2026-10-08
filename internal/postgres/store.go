// Package postgres implements durable admission and leased scheduling. The HTTP
// service can use this store directly, while cmd/small-chain-worker consumes it
// through the bounded database worker.
package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

const operationTimeout = 5 * time.Second

type Store struct{ pool *pgxpool.Pool }

// Open bounds connections and query/lock waits. Migrations are explicit: call
// Migrate with a schema-owner connection before using the store.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = operationTimeout
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "2000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Ping checks whether the existing bounded pool can reach PostgreSQL. It does
// not run migrations or mutate application data.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return nil
}

const jobColumns = "id, spec, state, attempts, result, error, created_at, updated_at"

func readJob(row pgx.Row) (jobs.Job, error) {
	var j jobs.Job
	var encoded []byte
	var result, errorMessage sql.NullString
	if err := row.Scan(&j.ID, &encoded, &j.State, &j.Attempts, &result, &errorMessage, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return jobs.Job{}, err
	}
	if result.Valid {
		j.Result = result.String
	}
	if errorMessage.Valid {
		j.Error = errorMessage.String
	}
	if err := decodeSpec(encoded, &j.Spec); err != nil {
		return jobs.Job{}, err
	}
	return j, nil
}

func decodeSpec(encoded []byte, spec *jobs.Spec) error {
	if err := json.Unmarshal(encoded, spec); err != nil {
		return fmt.Errorf("decode stored spec: %w", err)
	}
	return nil
}

// Submit persists one normalized request per key. The bool identifies replay.
// Existing keys replay even at capacity. Terminal keys remain stable for at
// least the configured replay window; after cleanup, reusing a key creates new
// work. A commit error is ambiguous: retry the same key.
func (s *Store) Submit(ctx context.Context, key string, spec jobs.Spec) (jobs.Job, bool, error) {
	if err := jobs.ValidateKey(key); err != nil {
		return jobs.Job{}, false, err
	}
	spec, err := spec.Normalize()
	if err != nil {
		return jobs.Job{}, false, err
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return jobs.Job{}, false, err
	}
	fingerprint := sha256.Sum256(encoded)
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return jobs.Job{}, false, err
	}
	id := hex.EncodeToString(random[:])
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return jobs.Job{}, false, fmt.Errorf("begin admission: %w", err)
	}
	defer rollback(tx)

	// Lock a visible key before attempting an insert. Cleanup then cannot
	// remove an old terminal replay between this read and commit.
	j, err := readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+`
 FROM jobs WHERE idempotency_key = $1 FOR UPDATE`, key))
	if err == nil {
		if j.Spec != spec {
			return jobs.Job{}, false, jobs.ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return jobs.Job{}, false, fmt.Errorf("commit admission replay: %w", err)
		}
		return j, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, false, fmt.Errorf("read admission key: %w", err)
	}

	j, err = readJob(tx.QueryRow(ctx, `INSERT INTO jobs (id, idempotency_key, request_fingerprint, spec, max_attempts)
 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (idempotency_key) DO NOTHING RETURNING `+jobColumns,
		id, key, fingerprint[:], string(encoded), spec.MaxAttempts))
	if isCapacityError(err) {
		return jobs.Job{}, false, jobs.ErrCapacity
	}
	replay := errors.Is(err, pgx.ErrNoRows)
	if replay {
		// A new READ COMMITTED statement sees and locks the winning concurrent
		// commit. Newly inserted rows cannot be retention-eligible, so cleanup
		// cannot remove the winner before this statement obtains its row lock.
		j, err = readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+`
 FROM jobs WHERE idempotency_key = $1 FOR UPDATE`, key))
		if err == nil && j.Spec != spec {
			return jobs.Job{}, false, jobs.ErrConflict
		}
	}
	if err != nil {
		return jobs.Job{}, false, fmt.Errorf("admit job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return jobs.Job{}, false, fmt.Errorf("commit admission (retry same key): %w", err)
	}
	return j, replay, nil
}

func isCapacityError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "admission_control_capacity"
}

func (s *Store) Get(ctx context.Context, id string) (jobs.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	j, err := readJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, fmt.Errorf("get job: %w", err)
	}
	return j, nil
}

func rollback(tx pgx.Tx) {
	// Caller cancellation must not prevent releasing a transaction/connection.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
