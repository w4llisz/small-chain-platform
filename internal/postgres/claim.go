package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

const (
	maxClaimBatch = 100
	minLease      = time.Second
	maxLease      = 15 * time.Minute
)

var validLeaseOwner = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Claim is the durable token returned to a worker. Owner and Version must
// accompany future heartbeats and terminal writes so a stale worker cannot
// mutate a job after reassignment.
type Claim struct {
	Job       jobs.Job
	Owner     string
	Version   int64
	ExpiresAt time.Time
}

// ClaimDue atomically leases up to limit due jobs in deterministic queue order.
// It uses PostgreSQL time and skips rows locked by other claimers. Execution
// must happen after this method returns; no task work belongs in the transaction.
func (s *Store) ClaimDue(ctx context.Context, owner string, limit int, lease time.Duration) ([]Claim, error) {
	if !validLeaseOwner.MatchString(owner) {
		return nil, fmt.Errorf("%w: lease owner must be 1..128 ASCII letters, digits, '.', '_', ':' or '-'", jobs.ErrInvalid)
	}
	if limit < 1 || limit > maxClaimBatch {
		return nil, fmt.Errorf("%w: claim limit must be 1..%d", jobs.ErrInvalid, maxClaimBatch)
	}
	if err := validateLeaseDuration(lease); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer rollback(tx)

	rows, err := tx.Query(ctx, `
WITH candidates AS (
    SELECT id
    FROM jobs
    WHERE state IN ('queued', 'retrying')
      AND available_at <= statement_timestamp()
      AND attempts < max_attempts
    ORDER BY available_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
), claimed AS (
    UPDATE jobs AS j
    SET state = 'running', result = NULL, error = NULL,
        attempts = j.attempts + 1,
        lease_owner = $2,
        lease_version = j.lease_version + 1,
        lease_expires_at = statement_timestamp() + $3::double precision * interval '1 microsecond',
        updated_at = statement_timestamp()
    FROM candidates AS c
    WHERE j.id = c.id
    RETURNING j.id, j.spec, j.state, j.attempts, j.result, j.error, j.created_at, j.updated_at,
              j.lease_owner, j.lease_version, j.lease_expires_at, j.available_at
)
SELECT id, spec, state, attempts, result, error, created_at, updated_at,
       lease_owner, lease_version, lease_expires_at
FROM claimed
ORDER BY available_at, id`, limit, owner, lease.Microseconds())
	if err != nil {
		return nil, fmt.Errorf("claim due jobs: %w", err)
	}
	defer rows.Close()

	claims := make([]Claim, 0, limit)
	for rows.Next() {
		claim, err := scanClaim(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claimed job: %w", err)
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read claimed jobs: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim (do not execute returned work): %w", err)
	}
	return claims, nil
}

func scanClaim(row pgx.Row) (Claim, error) {
	var claim Claim
	var encoded []byte
	var result, errorMessage sql.NullString
	if err := row.Scan(
		&claim.Job.ID, &encoded, &claim.Job.State, &claim.Job.Attempts,
		&result, &errorMessage, &claim.Job.CreatedAt, &claim.Job.UpdatedAt,
		&claim.Owner, &claim.Version, &claim.ExpiresAt,
	); err != nil {
		return Claim{}, err
	}
	if result.Valid {
		claim.Job.Result = result.String
	}
	if errorMessage.Valid {
		claim.Job.Error = errorMessage.String
	}
	if err := decodeSpec(encoded, &claim.Job.Spec); err != nil {
		return Claim{}, err
	}
	return claim, nil
}
