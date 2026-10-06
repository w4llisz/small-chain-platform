// Package dbworker executes trusted jobs claimed from PostgreSQL.
package dbworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

const maxPersistedErrorBytes = 1024

var validOwner = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type LeaseStore interface {
	ClaimDue(context.Context, string, int, time.Duration) ([]postgres.Claim, error)
	Heartbeat(context.Context, postgres.Claim, time.Duration) (postgres.Claim, error)
	Complete(context.Context, postgres.Claim, string) (jobs.Job, error)
	FailAttempt(context.Context, postgres.Claim, string, time.Duration) (jobs.Job, error)
	FailPermanent(context.Context, postgres.Claim, string) (jobs.Job, error)
	RecoverExpired(context.Context, int, time.Duration) ([]jobs.Job, error)
}

type Config struct {
	Owner             string
	Concurrency       int
	PollInterval      time.Duration
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	RetryBase         time.Duration
	RetryMax          time.Duration
	RecoveryInterval  time.Duration
	RecoveryBatch     int
}

func DefaultConfig(owner string) Config {
	return Config{
		Owner: owner, Concurrency: 4, PollInterval: 100 * time.Millisecond,
		LeaseDuration: 10 * time.Second, HeartbeatInterval: 3 * time.Second,
		RetryBase: 250 * time.Millisecond, RetryMax: 5 * time.Second,
		RecoveryInterval: time.Second, RecoveryBatch: 100,
	}
}

// Worker runs one polling loop and at most Config.Concurrency attempts. Run may
// be called once. Canceling its context stops claims/recovery, then drains active
// attempts; each attempt remains bounded by its persisted timeout.
type Worker struct {
	cfg     Config
	store   LeaseStore
	execute jobs.Executor
	log     *slog.Logger
	slots   chan struct{}
	wg      sync.WaitGroup
}

// ValidateConfig checks worker bounds without opening a database connection.
func ValidateConfig(cfg Config) error {
	if !validOwner.MatchString(cfg.Owner) {
		return errors.New("worker owner must be 1..128 ASCII letters, digits, '.', '_', ':' or '-'")
	}
	if cfg.Concurrency < 1 || cfg.Concurrency > 100 || cfg.RecoveryBatch < 1 || cfg.RecoveryBatch > 100 {
		return errors.New("worker concurrency and recovery batch must be between 1 and 100")
	}
	if cfg.PollInterval <= 0 || cfg.RecoveryInterval <= 0 || cfg.LeaseDuration < time.Second || cfg.LeaseDuration > 15*time.Minute {
		return errors.New("poll/recovery intervals must be positive and lease must be between 1 second and 15 minutes")
	}
	if cfg.HeartbeatInterval <= 0 || cfg.HeartbeatInterval > cfg.LeaseDuration/2 {
		return errors.New("heartbeat interval must be positive and at most half the lease duration")
	}
	if cfg.RetryBase <= 0 || cfg.RetryMax < cfg.RetryBase || cfg.RetryMax > 24*time.Hour {
		return errors.New("retry base must be positive and retry max must be between base and 24 hours")
	}
	return nil
}

func New(cfg Config, store LeaseStore, execute jobs.Executor, logger *slog.Logger) (*Worker, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if store == nil || execute == nil || logger == nil {
		return nil, errors.New("store, executor and logger are required")
	}
	return &Worker{cfg: cfg, store: store, execute: execute, log: logger, slots: make(chan struct{}, cfg.Concurrency)}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	poll := time.NewTicker(w.cfg.PollInterval)
	recover := time.NewTicker(w.cfg.RecoveryInterval)
	defer poll.Stop()
	defer recover.Stop()

	w.recover(ctx)
	w.claim(ctx)
	for {
		select {
		case <-ctx.Done():
			w.wg.Wait()
			return nil
		case <-poll.C:
			w.claim(ctx)
		case <-recover.C:
			w.recover(ctx)
		}
	}
}

func (w *Worker) claim(ctx context.Context) {
	available := cap(w.slots) - len(w.slots)
	if available == 0 || ctx.Err() != nil {
		return
	}
	claims, err := w.store.ClaimDue(ctx, w.cfg.Owner, available, w.cfg.LeaseDuration)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("dbworker.claim_failed", "error", err)
		}
		return
	}
	for _, claim := range claims {
		w.slots <- struct{}{}
		w.wg.Add(1)
		go func(claim postgres.Claim) {
			defer w.wg.Done()
			defer func() { <-w.slots }()
			w.runClaim(claim)
		}(claim)
	}
}

func (w *Worker) recover(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if _, err := w.store.RecoverExpired(ctx, w.cfg.RecoveryBatch, w.cfg.RetryBase); err != nil && ctx.Err() == nil {
		w.log.Warn("dbworker.recovery_failed", "error", err)
	}
}

type outcome struct {
	result string
	err    error
}

func (w *Worker) runClaim(claim postgres.Claim) {
	attemptCtx, cancel := context.WithTimeout(context.Background(), time.Duration(claim.Job.Spec.TimeoutMS)*time.Millisecond)
	defer cancel()
	done := make(chan outcome, 1)
	spec, attempt := claim.Job.Spec, claim.Job.Attempts
	go func() {
		result, err := w.invoke(attemptCtx, spec, attempt)
		done <- outcome{result: result, err: err}
	}()

	heartbeat := time.NewTicker(w.cfg.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case result := <-done:
			if err := attemptCtx.Err(); err != nil {
				result.err = err
			}
			cancel()
			w.persist(claim, result)
			return
		case <-heartbeat.C:
			renewed, err := w.store.Heartbeat(context.Background(), claim, w.cfg.LeaseDuration)
			if err == nil {
				claim = renewed
				continue
			}
			cancel()
			<-done // Executor contract requires prompt cancellation.
			if !errors.Is(err, postgres.ErrStaleLease) {
				w.log.Warn("dbworker.heartbeat_failed", "job_id", claim.Job.ID, "error", err)
			}
			return // Never write an outcome after losing the lease.
		}
	}
}

func (w *Worker) persist(claim postgres.Claim, result outcome) {
	var err error
	if result.err == nil {
		_, err = w.store.Complete(context.Background(), claim, result.result)
	} else if jobs.IsRetryable(result.err) {
		_, err = w.store.FailAttempt(context.Background(), claim, safeMessage(result.err), w.backoff(claim.Job.Attempts))
	} else {
		_, err = w.store.FailPermanent(context.Background(), claim, safeMessage(result.err))
	}
	if err != nil && !errors.Is(err, postgres.ErrStaleLease) {
		w.log.Warn("dbworker.outcome_failed", "job_id", claim.Job.ID, "error", err)
	}
}

func (w *Worker) invoke(ctx context.Context, spec jobs.Spec, attempt int) (result string, err error) {
	defer func() {
		if recover() != nil {
			result = ""
			err = errors.New("executor panic")
		}
	}()
	return w.execute(ctx, spec, attempt)
}

func (w *Worker) backoff(attempt int) time.Duration {
	d := w.cfg.RetryBase
	for i := 1; i < attempt; i++ {
		if d >= w.cfg.RetryMax/2 {
			d = w.cfg.RetryMax
			break
		}
		d *= 2
	}
	if d > w.cfg.RetryMax {
		d = w.cfg.RetryMax
	}
	return d/2 + time.Duration(mathrand.Int64N(int64(d-d/2)))
}

func safeMessage(err error) string {
	if err == nil {
		return "executor failed"
	}
	message := err.Error()
	if message == "" || !utf8.ValidString(message) {
		return "executor failed"
	}
	if len(message) <= maxPersistedErrorBytes {
		return message
	}
	end := maxPersistedErrorBytes
	for end > 0 && !utf8.ValidString(message[:end]) {
		end--
	}
	if end == 0 {
		return fmt.Sprintf("executor error exceeded %d bytes", maxPersistedErrorBytes)
	}
	return message[:end]
}
