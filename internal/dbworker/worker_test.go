package dbworker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

type recordedOutcome struct {
	claim   postgres.Claim
	value   string
	retryIn time.Duration
}

type fakeStore struct {
	mu           sync.Mutex
	claims       []postgres.Claim
	claimCalls   int
	heartbeats   int
	recoveries   int
	completed    []recordedOutcome
	retried      []recordedOutcome
	permanent    []recordedOutcome
	heartbeatErr error
}

func (s *fakeStore) ClaimDue(ctx context.Context, owner string, limit int, lease time.Duration) ([]postgres.Claim, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls++
	if limit > len(s.claims) {
		limit = len(s.claims)
	}
	claimed := append([]postgres.Claim(nil), s.claims[:limit]...)
	s.claims = s.claims[limit:]
	for i := range claimed {
		claimed[i].Owner = owner
		claimed[i].ExpiresAt = time.Now().Add(lease)
	}
	return claimed, nil
}

func (s *fakeStore) Heartbeat(_ context.Context, claim postgres.Claim, lease time.Duration) (postgres.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats++
	if s.heartbeatErr != nil {
		return postgres.Claim{}, s.heartbeatErr
	}
	claim.ExpiresAt = time.Now().Add(lease)
	return claim, nil
}

func (s *fakeStore) Complete(_ context.Context, claim postgres.Claim, result string) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, recordedOutcome{claim: claim, value: result})
	return claim.Job, nil
}

func (s *fakeStore) FailAttempt(_ context.Context, claim postgres.Claim, message string, retryIn time.Duration) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, recordedOutcome{claim: claim, value: message, retryIn: retryIn})
	return claim.Job, nil
}

func (s *fakeStore) FailPermanent(_ context.Context, claim postgres.Claim, message string) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permanent = append(s.permanent, recordedOutcome{claim: claim, value: message})
	return claim.Job, nil
}

func (s *fakeStore) RecoverExpired(ctx context.Context, _ int, _ time.Duration) ([]jobs.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.recoveries++
	s.mu.Unlock()
	return nil, nil
}

func TestWorkerPersistsSuccessRetryAndPanic(t *testing.T) {
	store := &fakeStore{claims: []postgres.Claim{
		claim("00000000000000000000000000000001", `{"mode":"success"}`),
		claim("00000000000000000000000000000002", `{"mode":"retry"}`),
		claim("00000000000000000000000000000003", `{"mode":"panic"}`),
	}}
	execute := func(_ context.Context, spec jobs.Spec, _ int) (string, error) {
		switch string(spec.Payload) {
		case `{"mode":"success"}`:
			return "digest", nil
		case `{"mode":"retry"}`:
			return "", jobs.Retryable(errors.New("temporary failure"))
		default:
			panic("sensitive panic value")
		}
	}
	worker := newTestWorker(t, store, execute, 3)
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(worker, ctx)
	waitFor(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.completed)+len(store.retried)+len(store.permanent) == 3
	})
	cancel()
	waitRun(t, done)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.completed) != 1 || store.completed[0].value != "digest" {
		t.Fatalf("completed = %#v", store.completed)
	}
	if len(store.retried) != 1 || store.retried[0].value != "temporary failure" {
		t.Fatalf("retried = %#v", store.retried)
	}
	if got := store.retried[0].retryIn; got < 10*time.Millisecond || got >= 20*time.Millisecond {
		t.Fatalf("retry delay = %s, want [10ms, 20ms)", got)
	}
	if len(store.permanent) != 1 || store.permanent[0].value != "executor panic" {
		t.Fatalf("permanent = %#v", store.permanent)
	}
	if store.recoveries == 0 {
		t.Fatal("worker did not perform its startup recovery sweep")
	}
}

func TestHeartbeatLossCancelsAttemptWithoutPersistingOutcome(t *testing.T) {
	store := &fakeStore{
		claims:       []postgres.Claim{claim("00000000000000000000000000000001", `{}`)},
		heartbeatErr: postgres.ErrStaleLease,
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	execute := func(ctx context.Context, _ jobs.Spec, _ int) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "late result", ctx.Err()
	}
	worker := newTestWorker(t, store, execute, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(worker, ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("heartbeat loss did not cancel executor")
	}
	cancel()
	waitRun(t, done)

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.heartbeats == 0 {
		t.Fatal("expected a heartbeat")
	}
	if len(store.completed)+len(store.retried)+len(store.permanent) != 0 {
		t.Fatal("worker persisted an outcome after losing its lease")
	}
}

func TestShutdownStopsClaimsAndDrainsBoundedAttempts(t *testing.T) {
	store := &fakeStore{claims: []postgres.Claim{
		claim("00000000000000000000000000000001", `{}`),
		claim("00000000000000000000000000000002", `{}`),
		claim("00000000000000000000000000000003", `{}`),
		claim("00000000000000000000000000000004", `{}`),
	}}
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var active atomic.Int32
	var maximum atomic.Int32
	execute := func(_ context.Context, _ jobs.Spec, _ int) (string, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return "done", nil
	}
	worker := newTestWorker(t, store, execute, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(worker, ctx)
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("bounded attempts did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned before attempts drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	waitRun(t, done)

	store.mu.Lock()
	defer store.mu.Unlock()
	if maximum.Load() != 2 || len(store.completed) != 2 || len(store.claims) != 2 {
		t.Fatalf("max active=%d completed=%d unclaimed=%d", maximum.Load(), len(store.completed), len(store.claims))
	}
	if store.claimCalls != 1 {
		t.Fatalf("claim calls = %d, want 1", store.claimCalls)
	}
}

func TestSafeMessageIsBoundedUTF8(t *testing.T) {
	message := strings.Repeat("界", 500)
	got := safeMessage(errors.New(message))
	if len(got) > maxPersistedErrorBytes || !utf8.ValidString(got) || !strings.HasPrefix(message, got) {
		t.Fatalf("safeMessage returned %d bytes, valid=%t", len(got), utf8.ValidString(got))
	}
	if got := safeMessage(errors.New(string([]byte{0xff}))); got != "executor failed" {
		t.Fatalf("invalid UTF-8 message = %q", got)
	}
}

func newTestWorker(t *testing.T, store LeaseStore, execute jobs.Executor, concurrency int) *Worker {
	t.Helper()
	cfg := DefaultConfig("test-worker")
	cfg.Concurrency = concurrency
	cfg.PollInterval = 5 * time.Millisecond
	cfg.LeaseDuration = time.Second
	cfg.HeartbeatInterval = 20 * time.Millisecond
	cfg.RetryBase = 20 * time.Millisecond
	cfg.RetryMax = 20 * time.Millisecond
	cfg.RecoveryInterval = time.Hour
	worker, err := New(cfg, store, execute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func claim(id, payload string) postgres.Claim {
	return postgres.Claim{
		Job: jobs.Job{
			ID:       id,
			Spec:     jobs.Spec{Payload: payload, TimeoutMS: 5_000, MaxAttempts: 3},
			State:    jobs.Running,
			Attempts: 1,
		},
		Version: 1,
	}
}

func runWorker(worker *Worker, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	return done
}

func waitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(time.Millisecond)
	}
}
