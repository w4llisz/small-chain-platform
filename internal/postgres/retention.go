package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

const (
	minAdmissionCapacity = 1
	maxAdmissionCapacity = 1_000_000
	minReplayWindow      = time.Minute
	maxReplayWindow      = 30 * 24 * time.Hour
	maxPurgeBatch        = 1000
)

// AdmissionPolicy is shared database state, not process-local configuration.
// RetainedJobs includes active and terminal records.
type AdmissionPolicy struct {
	RetainedJobs int
	MaxJobs      int
	ReplayWindow time.Duration
}

func (s *Store) AdmissionPolicy(ctx context.Context) (AdmissionPolicy, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	policy, err := scanAdmissionPolicy(s.pool.QueryRow(ctx, `SELECT retained_jobs, max_jobs,
 round(extract(epoch FROM replay_window) * 1000000)::bigint
FROM admission_control WHERE singleton`))
	if err != nil {
		return AdmissionPolicy{}, fmt.Errorf("read admission policy: %w", err)
	}
	return policy, nil
}

// ConfigureAdmission updates the shared cap and minimum replay window. It will
// not lower the cap below the number of currently retained records.
func (s *Store) ConfigureAdmission(ctx context.Context, maxJobs int, replayWindow time.Duration) (AdmissionPolicy, error) {
	if maxJobs < minAdmissionCapacity || maxJobs > maxAdmissionCapacity {
		return AdmissionPolicy{}, fmt.Errorf("%w: maximum jobs must be between %d and %d", jobs.ErrInvalid, minAdmissionCapacity, maxAdmissionCapacity)
	}
	if replayWindow < minReplayWindow || replayWindow > maxReplayWindow {
		return AdmissionPolicy{}, fmt.Errorf("%w: replay window must be between %s and %s", jobs.ErrInvalid, minReplayWindow, maxReplayWindow)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	policy, err := scanAdmissionPolicy(s.pool.QueryRow(ctx, `UPDATE admission_control
SET max_jobs = $1, replay_window = $2::double precision * interval '1 microsecond'
WHERE singleton AND retained_jobs <= $1
RETURNING retained_jobs, max_jobs,
          round(extract(epoch FROM replay_window) * 1000000)::bigint`,
		maxJobs, replayWindow.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return AdmissionPolicy{}, fmt.Errorf("%w: maximum jobs cannot be below the retained job count", jobs.ErrInvalid)
	}
	if err != nil {
		return AdmissionPolicy{}, fmt.Errorf("configure admission: %w", err)
	}
	return policy, nil
}

// PurgeTerminal removes at most limit terminal jobs whose last transition is
// outside the configured replay window. Active work is never eligible. The
// policy row is locked before selecting jobs so a concurrent policy change and
// this cleanup have one database order.
func (s *Store) PurgeTerminal(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > maxPurgeBatch {
		return 0, fmt.Errorf("%w: purge limit must be between 1 and %d", jobs.ErrInvalid, maxPurgeBatch)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin terminal purge: %w", err)
	}
	defer rollback(tx)

	var replayMicros int64
	if err := tx.QueryRow(ctx, `SELECT round(extract(epoch FROM replay_window) * 1000000)::bigint
FROM admission_control WHERE singleton FOR UPDATE`).Scan(&replayMicros); err != nil {
		return 0, fmt.Errorf("lock admission policy: %w", err)
	}
	rows, err := tx.Query(ctx, `WITH doomed AS (
    SELECT id
    FROM jobs
    WHERE state IN ('succeeded', 'failed', 'canceled')
      AND updated_at <= statement_timestamp() - $2::double precision * interval '1 microsecond'
    ORDER BY updated_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
DELETE FROM jobs AS j
USING doomed AS d
WHERE j.id = d.id
RETURNING j.id`, limit, replayMicros)
	if err != nil {
		return 0, fmt.Errorf("purge terminal jobs: %w", err)
	}
	deleted := 0
	for rows.Next() {
		deleted++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read terminal purge: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit terminal purge (count unknown): %w", err)
	}
	return deleted, nil
}

func scanAdmissionPolicy(row pgx.Row) (AdmissionPolicy, error) {
	var policy AdmissionPolicy
	var replayMicros int64
	if err := row.Scan(&policy.RetainedJobs, &policy.MaxJobs, &replayMicros); err != nil {
		return AdmissionPolicy{}, err
	}
	policy.ReplayWindow = time.Duration(replayMicros) * time.Microsecond
	return policy, nil
}
