package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestEngine(t *testing.T, cfg Config, executor Executor) *Engine {
	t.Helper()
	e, err := New(cfg, executor, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := e.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return e
}

func testConfig() Config {
	return Config{Workers: 2, QueueCapacity: 32, MaxJobs: 100, RetryBase: time.Millisecond, RetryMax: 4 * time.Millisecond}
}
func testSpec() Spec { return Spec{Kind: "demo.checksum", Payload: "hello"} }

func waitJob(t *testing.T, e *Engine, id string, want State) Job {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		j, err := e.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State == want {
			return j
		}
		if j.State.Terminal() {
			t.Fatalf("wanted %s, got %+v", want, j)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s: %+v", want, j)
		}
	}
}

func submit(t *testing.T, e *Engine, key string, spec Spec) Job {
	t.Helper()
	j, _, err := e.Submit(key, spec)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func receive(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
}

func TestConcurrentIdempotency(t *testing.T) {
	var executions atomic.Int32
	e := newTestEngine(t, testConfig(), func(ctx context.Context, s Spec, n int) (string, error) {
		executions.Add(1)
		return Checksum(ctx, s, n)
	})
	const clients = 32
	var wg sync.WaitGroup
	ids := make(chan string, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, _, err := e.Submit("same-key", testSpec())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- j.ID
		}()
	}
	wg.Wait()
	close(ids)
	var id string
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate jobs admitted")
		}
		id = got
	}
	j := waitJob(t, e, id, Succeeded)
	if executions.Load() != 1 || j.Attempts != 1 {
		t.Fatalf("executions=%d job=%+v", executions.Load(), j)
	}
	s := testSpec()
	s.Payload = "different"
	if _, _, err := e.Submit("same-key", s); !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v", err)
	}
	s = testSpec()
	s.MaxAttempts = 3
	s.TimeoutMS = 1000
	if _, replay, err := e.Submit("same-key", s); err != nil || !replay {
		t.Fatalf("normalized replay: %v %v", replay, err)
	}
}

func TestBoundedWorkersAndAdmission(t *testing.T) {
	cfg := testConfig()
	cfg.Workers = 2
	cfg.QueueCapacity = 1
	started := make(chan struct{}, 2)
	var active, peak atomic.Int32
	e := newTestEngine(t, cfg, func(ctx context.Context, _ Spec, _ int) (string, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	first := submit(t, e, "first", testSpec())
	receive(t, started)
	second := submit(t, e, "second", testSpec())
	receive(t, started)
	third := submit(t, e, "third", testSpec())
	if _, _, err := e.Submit("retry-key", testSpec()); !errors.Is(err, ErrFull) {
		t.Fatalf("got %v", err)
	}
	if peak.Load() != 2 || e.Stats().QueueDepth != 1 {
		t.Fatal("worker/queue bound violated")
	}
	_, _ = e.Cancel(third.ID) // A tombstone remains until a worker dequeues it.
	_, _ = e.Cancel(first.ID)
	_, _ = e.Cancel(second.ID)
	deadline := time.Now().Add(time.Second)
	for {
		j, _, err := e.Submit("retry-key", testSpec())
		if err == nil {
			receive(t, started)
			_, _ = e.Cancel(j.ID)
			break
		}
		if !errors.Is(err, ErrFull) || time.Now().After(deadline) {
			t.Fatalf("rejected key was consumed: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRetryClassificationAndPanic(t *testing.T) {
	cases := []struct {
		name     string
		fn       Executor
		attempts int
		state    State
	}{
		{"transient then success", func(_ context.Context, _ Spec, n int) (string, error) {
			if n < 3 {
				return "", Retryable(errors.New("temporary"))
			}
			return "ok", nil
		}, 3, Succeeded},
		{"retry exhausted", func(context.Context, Spec, int) (string, error) { return "", Retryable(errors.New("temporary")) }, 3, Failed},
		{"permanent", func(context.Context, Spec, int) (string, error) { return "", errors.New("permanent") }, 1, Failed},
		{"panic", func(context.Context, Spec, int) (string, error) { panic("secret") }, 1, Failed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, testConfig(), tc.fn)
			j := submit(t, e, "test", testSpec())
			j = waitJob(t, e, j.ID, tc.state)
			if j.Attempts != tc.attempts || e.Stats().Retries != uint64(tc.attempts-1) {
				t.Fatalf("%+v", j)
			}
			if j.Error == "secret" {
				t.Fatal("panic leaked")
			}
		})
	}
}

func TestTimeoutAndLateSuccess(t *testing.T) {
	e := newTestEngine(t, testConfig(), func(ctx context.Context, _ Spec, _ int) (string, error) { <-ctx.Done(); return "late", nil })
	s := testSpec()
	s.TimeoutMS = 5
	j := submit(t, e, "timeout", s)
	j = waitJob(t, e, j.ID, Failed)
	if j.Attempts != 1 || j.Result != "" || j.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("%+v", j)
	}
}

func TestCancelQueuedRunningAndRetrying(t *testing.T) {
	for _, state := range []State{Queued, Running, Retrying} {
		t.Run(string(state), func(t *testing.T) {
			cfg := testConfig()
			cfg.Workers = 1
			cfg.RetryBase = time.Hour
			cfg.RetryMax = time.Hour
			started := make(chan struct{}, 2)
			e := newTestEngine(t, cfg, func(ctx context.Context, _ Spec, _ int) (string, error) {
				started <- struct{}{}
				if state == Retrying {
					return "", Retryable(errors.New("retry"))
				}
				<-ctx.Done()
				return "late success", nil
			})
			first := submit(t, e, "first", testSpec())
			receive(t, started)
			j := first
			if state == Queued {
				j = submit(t, e, "queued", testSpec())
			}
			waitJob(t, e, j.ID, state)
			if _, err := e.Cancel(j.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Cancel(j.ID); err != nil {
				t.Fatalf("cancel not idempotent: %v", err)
			}
			_, _ = e.Cancel(first.ID)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := e.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			got, _ := e.Get(j.ID)
			if got.State != Canceled || got.Result != "" {
				t.Fatalf("%+v", got)
			}
			if state == Queued && got.Attempts != 0 {
				t.Fatal("canceled queued task ran")
			}
		})
	}
}

func TestDrainAndForcedShutdown(t *testing.T) {
	t.Run("drain", func(t *testing.T) {
		e := newTestEngine(t, testConfig(), Checksum)
		j := submit(t, e, "before", testSpec())
		e.StopAdmission()
		if _, _, err := e.Submit("after", testSpec()); !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := e.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		waitJob(t, e, j.ID, Succeeded)
		if _, replay, err := e.Submit("before", testSpec()); err != nil || !replay {
			t.Fatal("replay unavailable during drain")
		}
	})
	t.Run("deadline", func(t *testing.T) {
		cfg := testConfig()
		cfg.Workers = 1
		started := make(chan struct{}, 1)
		e := newTestEngine(t, cfg, func(ctx context.Context, _ Spec, _ int) (string, error) {
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		})
		j := submit(t, e, "running", testSpec())
		receive(t, started)
		queued := submit(t, e, "queued", testSpec())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := e.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		waitJob(t, e, j.ID, Canceled)
		waitJob(t, e, queued.ID, Canceled)
	})
}

func TestRetentionCapacityAndTerminalImmutability(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	e := newTestEngine(t, cfg, Checksum)
	j := submit(t, e, "one", testSpec())
	waitJob(t, e, j.ID, Succeeded)
	if _, _, err := e.Submit("two", testSpec()); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, replay, err := e.Submit("one", testSpec()); err != nil || !replay {
		t.Fatal("capacity broke replay")
	}
	if _, err := e.Cancel(j.ID); !errors.Is(err, ErrTerminal) {
		t.Fatal(err)
	}
}

func TestBackoffBounds(t *testing.T) {
	e := newTestEngine(t, testConfig(), Checksum)
	for attempt, want := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond} {
		for i := 0; i < 100; i++ {
			d := e.backoff(attempt + 1)
			if d < want/2 || d >= want {
				t.Fatalf("attempt %d: %v", attempt+1, d)
			}
		}
	}
}

func TestValidation(t *testing.T) {
	e := newTestEngine(t, testConfig(), Checksum)
	for _, key := range []string{"", "contains space", "汉字"} {
		if _, _, err := e.Submit(key, testSpec()); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	s := testSpec()
	s.Kind = "shell"
	if _, _, err := e.Submit("invalid", s); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := e.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := e.Cancel("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestConcurrentSubmitAndShutdown(t *testing.T) {
	e := newTestEngine(t, testConfig(), Checksum)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := e.Submit(fmt.Sprintf("race-%d", i), testSpec())
			if err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrFull) {
				t.Error(err)
			}
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; e.StopAdmission() }()
	close(start)
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	stats := e.Stats()
	if stats.Accepting || stats.QueueDepth != 0 || stats.States[Queued]+stats.States[Running]+stats.States[Retrying] != 0 {
		t.Fatalf("unfinished work after drain: %+v", stats)
	}
}

func FuzzNormalize(f *testing.F) {
	f.Add("demo.checksum", "hello", 0, 0, 3, 1000)
	f.Add("shell", "", -1, 7, 100, 0)
	f.Fuzz(func(t *testing.T, kind, payload string, delay, fail, attempts, timeout int) {
		s, err := (Spec{kind, payload, delay, fail, attempts, timeout}).Normalize()
		if err != nil {
			return
		}
		if s.Kind != "demo.checksum" || len(s.Payload) > 4096 || s.DelayMS < 0 || s.DelayMS > 5000 || s.FailFirstAttempts < 0 || s.FailFirstAttempts > 5 || s.MaxAttempts < 1 || s.MaxAttempts > 5 || s.TimeoutMS < 1 || s.TimeoutMS > 30000 {
			t.Fatalf("invalid spec accepted: %+v", s)
		}
	})
}
