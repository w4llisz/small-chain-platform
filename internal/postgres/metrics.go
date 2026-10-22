package postgres

import (
	"context"
	"fmt"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

// JobCounts is a point-in-time database snapshot. Retention can change the
// values immediately after the query returns, so callers must not treat it as
// an accounting ledger.
type JobCounts map[jobs.State]int64

// CountJobsByState reads all six lifecycle states in one bounded query. The
// retained-job admission cap bounds the table this aggregation scans.
func (s *Store) CountJobsByState(ctx context.Context) (JobCounts, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	counts := JobCounts{
		jobs.Queued: 0, jobs.Running: 0, jobs.Retrying: 0,
		jobs.Succeeded: 0, jobs.Failed: 0, jobs.Canceled: 0,
	}
	rows, err := s.pool.Query(ctx, `SELECT state, count(*) FROM jobs GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("count jobs by state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state jobs.State
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("scan job state count: %w", err)
		}
		if _, known := counts[state]; !known {
			return nil, fmt.Errorf("count jobs by state: unknown state %q", state)
		}
		counts[state] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count jobs by state: %w", err)
	}
	return counts, nil
}
