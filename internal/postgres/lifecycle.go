package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

const (
	maxResultBytes = 4096
	maxErrorBytes  = 1024
	maxRetryDelay  = 24 * time.Hour
)

var (
	ErrStaleLease = errors.New("stale or expired lease")
	validJobID    = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Heartbeat extends an unexpired lease using database time. It cannot revive an
// expired lease, and owner/version mismatches are reported as ErrStaleLease.
func (s *Store) Heartbeat(ctx context.Context, claim Claim, lease time.Duration) (Claim, error) {
	if err := validateClaim(claim); err != nil {
		return Claim{}, err
	}
	if err := validateLeaseDuration(lease); err != nil {
		return Claim{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	updated, err := scanClaim(s.pool.QueryRow(ctx, `UPDATE jobs
SET lease_expires_at = statement_timestamp() + $4::double precision * interval '1 microsecond',
    updated_at = statement_timestamp()
WHERE id = $1 AND lease_owner = $2 AND lease_version = $3
  AND state = 'running' AND lease_expires_at > statement_timestamp()
RETURNING id, spec, state, attempts, result, error, created_at, updated_at,
          lease_owner, lease_version, lease_expires_at`,
		claim.Job.ID, claim.Owner, claim.Version, lease.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, ErrStaleLease
	}
	if err != nil {
		return Claim{}, fmt.Errorf("heartbeat lease: %w", err)
	}
	return updated, nil
}

// Complete commits a successful result only while the exact lease is current.
func (s *Store) Complete(ctx context.Context, claim Claim, result string) (jobs.Job, error) {
	if err := validateClaim(claim); err != nil {
		return jobs.Job{}, err
	}
	if !utf8.ValidString(result) || len(result) > maxResultBytes {
		return jobs.Job{}, fmt.Errorf("%w: result must be valid UTF-8 and at most %d bytes", jobs.ErrInvalid, maxResultBytes)
	}
	return s.finish(ctx, claim, `UPDATE jobs
SET state = 'succeeded', result = $4, error = NULL,
    lease_owner = NULL, lease_expires_at = NULL, updated_at = statement_timestamp()
WHERE id = $1 AND lease_owner = $2 AND lease_version = $3
  AND state = 'running' AND lease_expires_at > statement_timestamp()
RETURNING `+jobColumns, result)
}

// FailAttempt records an attempt error. Work with remaining attempts becomes
// retrying at database-time plus retryAfter; an exhausted job becomes failed.
func (s *Store) FailAttempt(ctx context.Context, claim Claim, message string, retryAfter time.Duration) (jobs.Job, error) {
	if err := validateClaim(claim); err != nil {
		return jobs.Job{}, err
	}
	if err := validateMessage(message); err != nil {
		return jobs.Job{}, err
	}
	if retryAfter < 0 || retryAfter > maxRetryDelay {
		return jobs.Job{}, fmt.Errorf("%w: retry delay must be between 0 and %s", jobs.ErrInvalid, maxRetryDelay)
	}
	return s.finish(ctx, claim, `UPDATE jobs
SET state = CASE WHEN attempts < max_attempts THEN 'retrying' ELSE 'failed' END,
    available_at = CASE WHEN attempts < max_attempts
        THEN statement_timestamp() + $4::double precision * interval '1 microsecond'
        ELSE available_at END,
    result = NULL, error = $5, lease_owner = NULL, lease_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE id = $1 AND lease_owner = $2 AND lease_version = $3
  AND state = 'running' AND lease_expires_at > statement_timestamp()
RETURNING `+jobColumns, retryAfter.Microseconds(), message)
}

// Cancel is an authoritative operator transition, not a worker lease action.
// It increments the version before clearing a running lease, fencing its worker.
func (s *Store) Cancel(ctx context.Context, id, reason string) (jobs.Job, error) {
	if !validJobID.MatchString(id) {
		return jobs.Job{}, fmt.Errorf("%w: job ID must be 32 lowercase hexadecimal characters", jobs.ErrInvalid)
	}
	if err := validateMessage(reason); err != nil {
		return jobs.Job{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	job, err := readJob(s.pool.QueryRow(ctx, `UPDATE jobs
SET state = 'canceled', result = NULL, error = $2,
    lease_owner = NULL, lease_expires_at = NULL,
    lease_version = lease_version + 1, updated_at = statement_timestamp()
WHERE id = $1 AND state IN ('queued', 'running', 'retrying')
RETURNING `+jobColumns, id, reason))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, fmt.Errorf("cancel job: %w", err)
	}
	job, err = s.Get(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	if job.State == jobs.Canceled {
		return job, nil
	}
	if job.State.Terminal() {
		return jobs.Job{}, jobs.ErrTerminal
	}
	return jobs.Job{}, errors.New("cancel job lost a concurrent state transition")
}

func (s *Store) finish(ctx context.Context, claim Claim, query string, args ...any) (jobs.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	params := []any{claim.Job.ID, claim.Owner, claim.Version}
	params = append(params, args...)
	job, err := readJob(s.pool.QueryRow(ctx, query, params...))
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, ErrStaleLease
	}
	if err != nil {
		return jobs.Job{}, fmt.Errorf("finish attempt: %w", err)
	}
	return job, nil
}

func validateClaim(claim Claim) error {
	if !validJobID.MatchString(claim.Job.ID) || !validLeaseOwner.MatchString(claim.Owner) || claim.Version < 1 {
		return fmt.Errorf("%w: claim requires a valid job ID, owner and positive version", jobs.ErrInvalid)
	}
	return nil
}

func validateLeaseDuration(lease time.Duration) error {
	if lease < minLease || lease > maxLease {
		return fmt.Errorf("%w: lease duration must be between %s and %s", jobs.ErrInvalid, minLease, maxLease)
	}
	return nil
}

func validateMessage(message string) error {
	if message == "" || !utf8.ValidString(message) || len(message) > maxErrorBytes {
		return fmt.Errorf("%w: message must be valid UTF-8 and 1..%d bytes", jobs.ErrInvalid, maxErrorBytes)
	}
	return nil
}
