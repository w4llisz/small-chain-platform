package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"regexp"
	"sync"
	"time"
)

type Config struct {
	Workers       int
	QueueCapacity int
	MaxJobs       int
	RetryBase     time.Duration
	RetryMax      time.Duration
}

type entry struct {
	job    Job
	ctx    context.Context
	cancel context.CancelFunc
}

// Engine owns all state. A single mutex serializes admission, cancellation and
// transitions; execution never holds it. The bounded channel owns pending IDs.
type Engine struct {
	mu        sync.Mutex
	cfg       Config
	execute   Executor
	log       *slog.Logger
	queue     chan string
	entries   map[string]*entry
	keys      map[string]string
	accepting bool
	root      context.Context
	stop      context.CancelFunc
	done      chan struct{}
	wg        sync.WaitGroup
	attempts  uint64
	retries   uint64
	rejected  uint64
	completed uint64
	duration  float64
}

var validKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func New(cfg Config, execute Executor, logger *slog.Logger) (*Engine, error) {
	if cfg.Workers < 1 || cfg.QueueCapacity < 1 || cfg.MaxJobs < 1 || cfg.RetryBase <= 0 || cfg.RetryMax < cfg.RetryBase {
		return nil, errors.New("workers, queue capacity, max jobs and retry base must be positive; retry max must be >= base")
	}
	if execute == nil || logger == nil {
		return nil, errors.New("executor and logger are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{cfg: cfg, execute: execute, log: logger, queue: make(chan string, cfg.QueueCapacity), entries: make(map[string]*entry), keys: make(map[string]string), accepting: true, root: ctx, stop: cancel, done: make(chan struct{})}
	for i := 0; i < cfg.Workers; i++ {
		e.wg.Add(1)
		go e.worker()
	}
	go func() { e.wg.Wait(); close(e.done) }()
	return e, nil
}

// Submit is atomic across idempotency lookup, capacity check and queue admission.
// Rejected requests do not consume their key. Replays work even while draining.
func (e *Engine) Submit(key string, spec Spec) (Job, bool, error) {
	if !validKey.MatchString(key) {
		return Job{}, false, fmt.Errorf("%w: Idempotency-Key must be 1..128 ASCII letters, digits, '.', '_', ':' or '-'", ErrInvalid)
	}
	spec, err := spec.Normalize()
	if err != nil {
		return Job{}, false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if id, ok := e.keys[key]; ok {
		j := e.entries[id].job
		if j.Spec != spec {
			return Job{}, false, ErrConflict
		}
		return j, true, nil
	}
	if !e.accepting {
		e.rejected++
		return Job{}, false, ErrClosed
	}
	if len(e.entries) >= e.cfg.MaxJobs {
		e.rejected++
		return Job{}, false, ErrCapacity
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return Job{}, false, fmt.Errorf("generate ID: %w", err)
	}
	id := hex.EncodeToString(idBytes[:])
	now := time.Now().UTC()
	j := Job{ID: id, Spec: spec, State: Queued, CreatedAt: now, UpdatedAt: now}
	select {
	case e.queue <- id:
		ctx, cancel := context.WithCancel(e.root)
		e.entries[id] = &entry{job: j, ctx: ctx, cancel: cancel}
		e.keys[key] = id
		e.log.Info("job.accepted", "job_id", id)
		return j, false, nil
	default:
		e.rejected++
		return Job{}, false, ErrFull
	}
}

func (e *Engine) Get(id string) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.entries[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return r.job, nil
}

func (e *Engine) Cancel(id string) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.entries[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	if r.job.State == Canceled {
		return r.job, nil
	}
	if r.job.State.Terminal() {
		return Job{}, ErrTerminal
	}
	r.cancel()
	e.transition(r, Canceled, "canceled by request")
	return r.job, nil
}

// StopAdmission closes the queue exactly once; workers drain accepted work.
func (e *Engine) StopAdmission() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.accepting {
		e.accepting = false
		close(e.queue)
	}
}

// Shutdown drains until ctx expires, then cancels all remaining work. Executors
// that ignore cancellation cannot be forcibly stopped in a Go goroutine.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.StopAdmission()
	select {
	case <-e.done:
		e.stop()
		return nil
	case <-ctx.Done():
		e.mu.Lock()
		e.stop()
		for _, r := range e.entries {
			if !r.job.State.Terminal() {
				r.cancel()
				e.transition(r, Canceled, "shutdown deadline exceeded")
			}
		}
		e.mu.Unlock()
		return ctx.Err()
	}
}

func (e *Engine) worker() {
	defer e.wg.Done()
	for id := range e.queue {
		e.run(id)
	}
}

func (e *Engine) run(id string) {
	e.mu.Lock()
	r := e.entries[id]
	if r.job.State.Terminal() {
		e.mu.Unlock()
		return
	}
	spec, ctx := r.job.Spec, r.ctx
	e.mu.Unlock()
	defer r.cancel()
	for attempt := 1; attempt <= spec.MaxAttempts; attempt++ {
		e.mu.Lock()
		if r.job.State.Terminal() {
			e.mu.Unlock()
			return
		}
		r.job.Attempts = attempt
		e.attempts++
		e.transition(r, Running, "")
		e.mu.Unlock()
		attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutMS)*time.Millisecond)
		started := time.Now()
		result, err := e.invoke(attemptCtx, spec, attempt)
		// A late result must not turn a timed-out attempt into success.
		if attemptCtx.Err() != nil {
			err = attemptCtx.Err()
		}
		cancel()
		e.mu.Lock()
		e.completed++
		e.duration += time.Since(started).Seconds()
		if r.job.State.Terminal() {
			e.mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			e.transition(r, Canceled, "execution canceled")
			e.mu.Unlock()
			return
		}
		if err == nil {
			r.job.Result = result
			e.transition(r, Succeeded, "")
			e.mu.Unlock()
			return
		}
		if !isRetryable(err) || attempt == spec.MaxAttempts {
			e.transition(r, Failed, err.Error())
			e.mu.Unlock()
			return
		}
		e.retries++
		e.transition(r, Retrying, err.Error())
		e.mu.Unlock()
		timer := time.NewTimer(e.backoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (e *Engine) invoke(ctx context.Context, spec Spec, attempt int) (result string, err error) {
	defer func() {
		if p := recover(); p != nil {
			// Do not expose panic values: an executor might include sensitive input.
			e.log.Error("executor.panic", "attempt", attempt)
			err = errors.New("executor panic")
		}
	}()
	return e.execute(ctx, spec, attempt)
}

func (e *Engine) backoff(attempt int) time.Duration {
	d := e.cfg.RetryBase
	for i := 1; i < attempt; i++ {
		if d >= e.cfg.RetryMax/2 {
			d = e.cfg.RetryMax
			break
		}
		d *= 2
	}
	if d > e.cfg.RetryMax {
		d = e.cfg.RetryMax
	}
	// Equal jitter in [d/2, d), bounded without overflowing Int64N.
	return d/2 + time.Duration(mathrand.Int64N(int64(d-d/2)))
}

// transition requires e.mu; terminal states never transition again.
func (e *Engine) transition(r *entry, state State, message string) {
	r.job.State, r.job.Error, r.job.UpdatedAt = state, message, time.Now().UTC()
	e.log.Info("job.transition", "job_id", r.job.ID, "state", state, "attempt", r.job.Attempts)
}

type Stats struct {
	Accepting       bool
	QueueDepth      int
	Workers         int
	MaxJobs         int
	States          map[State]int
	Attempts        uint64
	Retries         uint64
	Rejected        uint64
	Completed       uint64
	DurationSeconds float64
}

func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Stats{Accepting: e.accepting, QueueDepth: len(e.queue), Workers: e.cfg.Workers, MaxJobs: e.cfg.MaxJobs, States: map[State]int{}, Attempts: e.attempts, Retries: e.retries, Rejected: e.rejected, Completed: e.completed, DurationSeconds: e.duration}
	for _, r := range e.entries {
		s.States[r.job.State]++
	}
	return s
}
