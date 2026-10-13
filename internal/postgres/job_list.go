package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

const MaxJobPageSize = 100

// JobSummary is the bounded list representation used by the operations
// console. Full specs and results remain available from the single-job route.
type JobSummary struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	State       jobs.State `json:"state"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// JobPosition is the exclusive keyset position of the final returned row.
type JobPosition struct {
	CreatedAt time.Time
	ID        string
}

type JobListOptions struct {
	State  jobs.State
	Before *JobPosition
	Limit  int
}

type JobPage struct {
	Jobs []JobSummary
	Next *JobPosition
}

// ListJobs returns newest jobs first. The composite (created_at, id) cursor is
// stable when timestamps tie and does not shift when newer rows are inserted.
func (s *Store) ListJobs(ctx context.Context, options JobListOptions) (JobPage, error) {
	if options.Limit < 1 || options.Limit > MaxJobPageSize {
		return JobPage{}, fmt.Errorf("%w: job page limit must be 1..%d", jobs.ErrInvalid, MaxJobPageSize)
	}
	if options.State != "" && !knownState(options.State) {
		return JobPage{}, fmt.Errorf("%w: unknown job state", jobs.ErrInvalid)
	}
	if options.Before != nil && (options.Before.CreatedAt.IsZero() || !validJobID.MatchString(options.Before.ID)) {
		return JobPage{}, fmt.Errorf("%w: invalid job cursor", jobs.ErrInvalid)
	}

	query := `SELECT id, spec->>'kind', state, attempts, max_attempts, error,
       created_at, updated_at
FROM jobs`
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if options.State != "" {
		args = append(args, options.State)
		conditions = append(conditions, fmt.Sprintf("state = $%d", len(args)))
	}
	if options.Before != nil {
		args = append(args, options.Before.CreatedAt.UTC(), options.Before.ID)
		conditions = append(conditions, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	if len(conditions) > 0 {
		query += "\nWHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, options.Limit+1)
	query += fmt.Sprintf("\nORDER BY created_at DESC, id DESC\nLIMIT $%d", len(args))

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return JobPage{}, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	page := JobPage{Jobs: make([]JobSummary, 0, options.Limit)}
	for rows.Next() {
		var summary JobSummary
		var errorMessage sql.NullString
		if err := rows.Scan(&summary.ID, &summary.Kind, &summary.State, &summary.Attempts,
			&summary.MaxAttempts, &errorMessage, &summary.CreatedAt, &summary.UpdatedAt); err != nil {
			return JobPage{}, fmt.Errorf("scan job summary: %w", err)
		}
		if errorMessage.Valid {
			summary.Error = errorMessage.String
		}
		summary.CreatedAt = summary.CreatedAt.UTC()
		summary.UpdatedAt = summary.UpdatedAt.UTC()
		page.Jobs = append(page.Jobs, summary)
	}
	if err := rows.Err(); err != nil {
		return JobPage{}, fmt.Errorf("read job page: %w", err)
	}
	if len(page.Jobs) > options.Limit {
		page.Jobs = page.Jobs[:options.Limit]
		last := page.Jobs[len(page.Jobs)-1]
		page.Next = &JobPosition{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func knownState(state jobs.State) bool {
	switch state {
	case jobs.Queued, jobs.Running, jobs.Retrying, jobs.Succeeded, jobs.Failed, jobs.Canceled:
		return true
	default:
		return false
	}
}
