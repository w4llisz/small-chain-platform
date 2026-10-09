package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const maxBatch = 1000

// TerminalPurger removes a bounded batch of terminal jobs.
type TerminalPurger interface {
	PurgeTerminal(context.Context, int) (int, error)
}

// Retention runs one purge at startup and then on a fixed cadence. Calls are
// deliberately serial so a slow database never creates overlapping sweeps.
type Retention struct {
	purger   TerminalPurger
	logger   *slog.Logger
	interval time.Duration
	batch    int
}

func NewRetention(purger TerminalPurger, logger *slog.Logger, interval time.Duration, batch int) (*Retention, error) {
	if purger == nil {
		return nil, errors.New("retention purger is required")
	}
	if logger == nil {
		return nil, errors.New("retention logger is required")
	}
	if interval <= 0 {
		return nil, errors.New("retention interval must be positive")
	}
	if batch < 1 || batch > maxBatch {
		return nil, fmt.Errorf("retention batch must be between 1 and %d", maxBatch)
	}
	return &Retention{purger: purger, logger: logger, interval: interval, batch: batch}, nil
}

// Run blocks until ctx is canceled. Purge failures are observable but do not
// stop later sweeps or change HTTP readiness.
func (r *Retention) Run(ctx context.Context) {
	r.sweep(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

func (r *Retention) sweep(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	deleted, err := r.purger.PurgeTerminal(ctx, r.batch)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Error("maintenance.retention_failed", "error", err, "batch_limit", r.batch)
		}
		return
	}
	if deleted > 0 {
		r.logger.Info("maintenance.retention_purged", "deleted", deleted, "batch_limit", r.batch)
	}
}
