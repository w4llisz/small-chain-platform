package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

const leaseExpiredMessage = "worker lease expired before completion"

// RecoverExpired atomically releases a bounded batch of expired running jobs.
// Attempts with budget remaining become delayed retries; exhausted jobs fail.
// The version advances before the old lease is cleared, fencing stale workers.
func (s *Store) RecoverExpired(ctx context.Context, limit int, retryAfter time.Duration) ([]jobs.Job, error) {
	if limit < 1 || limit > maxClaimBatch {
		return nil, fmt.Errorf("%w: recovery limit must be 1..%d", jobs.ErrInvalid, maxClaimBatch)
	}
	if retryAfter < 0 || retryAfter > maxRetryDelay {
		return nil, fmt.Errorf("%w: recovery retry delay must be between 0 and %s", jobs.ErrInvalid, maxRetryDelay)
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
WITH candidates AS (
    SELECT id
    FROM jobs
    WHERE state = 'running' AND lease_expires_at <= statement_timestamp()
    ORDER BY lease_expires_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
UPDATE jobs AS j
SET state = CASE WHEN j.attempts < j.max_attempts THEN 'retrying' ELSE 'failed' END,
    available_at = CASE WHEN j.attempts < j.max_attempts
        THEN statement_timestamp() + $2::double precision * interval '1 microsecond'
        ELSE j.available_at END,
    result = NULL, error = $3,
    lease_owner = NULL, lease_expires_at = NULL,
    lease_version = j.lease_version + 1,
    updated_at = statement_timestamp()
FROM candidates AS c
WHERE j.id = c.id
RETURNING j.id, j.spec, j.state, j.attempts, j.result, j.error, j.created_at, j.updated_at`,
		limit, retryAfter.Microseconds(), leaseExpiredMessage)
	if err != nil {
		return nil, fmt.Errorf("recover expired leases: %w", err)
	}
	defer rows.Close()

	recovered := make([]jobs.Job, 0, limit)
	for rows.Next() {
		job, err := readJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan recovered job: %w", err)
		}
		recovered = append(recovered, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read recovered jobs: %w", err)
	}
	return recovered, nil
}
