package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

const MaxEventPageSize = 100

// Event is immutable lifecycle evidence retained with its job. Lease fields
// identify the worker or fence associated with a transition when applicable.
type Event struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	State        jobs.State `json:"state"`
	Attempt      int        `json:"attempt"`
	LeaseOwner   string     `json:"lease_owner,omitempty"`
	LeaseVersion int64      `json:"lease_version,omitempty"`
	Message      string     `json:"message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// EventPage advances strictly by event identity. NextCursor is omitted when
// the current snapshot has no later row; callers may repeat the last cursor to
// observe lifecycle events appended afterwards.
type EventPage struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// ListEvents reads a stable, bounded forward page. The jobs-to-events lateral
// join distinguishes an existing job with no rows after the cursor from a
// missing job in one MVCC snapshot, including during concurrent retention.
func (s *Store) ListEvents(ctx context.Context, jobID string, afterID int64, limit int) (EventPage, error) {
	if !validJobID.MatchString(jobID) {
		return EventPage{}, fmt.Errorf("%w: job ID must be 32 lowercase hexadecimal characters", jobs.ErrInvalid)
	}
	if afterID < 0 {
		return EventPage{}, fmt.Errorf("%w: event cursor must be non-negative", jobs.ErrInvalid)
	}
	if limit < 1 || limit > MaxEventPageSize {
		return EventPage{}, fmt.Errorf("%w: event page limit must be 1..%d", jobs.ErrInvalid, MaxEventPageSize)
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.kind, e.state, e.attempt,
       e.lease_owner, e.lease_version, e.message, e.created_at
FROM jobs AS j
LEFT JOIN LATERAL (
    SELECT id, kind, state, attempt, lease_owner, lease_version, message, created_at
    FROM job_events
    WHERE job_id = j.id AND id > $2
    ORDER BY id
    LIMIT $3
) AS e ON true
WHERE j.id = $1
ORDER BY e.id`, jobID, afterID, limit+1)
	if err != nil {
		return EventPage{}, fmt.Errorf("list job events: %w", err)
	}
	defer rows.Close()

	page := EventPage{Events: make([]Event, 0, limit)}
	foundJob := false
	for rows.Next() {
		foundJob = true
		var id, leaseVersion sql.NullInt64
		var kind, state, leaseOwner, message sql.NullString
		var attempt sql.NullInt64
		var createdAt sql.NullTime
		if err := rows.Scan(&id, &kind, &state, &attempt, &leaseOwner, &leaseVersion, &message, &createdAt); err != nil {
			return EventPage{}, fmt.Errorf("scan job event: %w", err)
		}
		if !id.Valid {
			continue
		}
		page.Events = append(page.Events, Event{
			ID:           strconv.FormatInt(id.Int64, 10),
			Kind:         kind.String,
			State:        jobs.State(state.String),
			Attempt:      int(attempt.Int64),
			LeaseOwner:   leaseOwner.String,
			LeaseVersion: leaseVersion.Int64,
			Message:      message.String,
			CreatedAt:    createdAt.Time.UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, fmt.Errorf("read job events: %w", err)
	}
	if !foundJob {
		return EventPage{}, jobs.ErrNotFound
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		page.NextCursor = page.Events[len(page.Events)-1].ID
	}
	return page, nil
}
