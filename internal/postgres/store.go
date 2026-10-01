// Package postgres implements durable admission. It is not yet a scheduler:
// the HTTP service and workers still use the M1 in-memory engine.
package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

const jobColumns = "id, spec, state, attempts, created_at, updated_at"

func readJob(row pgx.Row) (jobs.Job, error) {
	var j jobs.Job
	var encoded []byte
	if err := row.Scan(&j.ID, &encoded, &j.State, &j.Attempts, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return jobs.Job{}, err
	}
	if err := json.Unmarshal(encoded, &j.Spec); err != nil {
		return jobs.Job{}, fmt.Errorf("decode stored spec: %w", err)
	}
	return j, nil
}

// Submit persists one normalized request per key. The bool identifies replay.
// Keys are retained indefinitely in this admission-only increment; no deletion
// path runs concurrently. A commit error is ambiguous: retry the same key.
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
	j, err := readJob(tx.QueryRow(ctx, `INSERT INTO jobs (id, idempotency_key, request_fingerprint, spec)
 VALUES ($1, $2, $3, $4) ON CONFLICT (idempotency_key) DO NOTHING RETURNING `+jobColumns,
		id, key, fingerprint[:], string(encoded)))
	replay := errors.Is(err, pgx.ErrNoRows)
	if replay {
		// A new READ COMMITTED statement sees the winning concurrent commit. A
		// single CTE/UNION SELECT could miss it in the INSERT statement's snapshot.
		j, err = readJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE idempotency_key = $1`, key))
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
