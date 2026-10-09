package maintenance

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type purgeFunc func(context.Context, int) (int, error)

func (f purgeFunc) PurgeTerminal(ctx context.Context, limit int) (int, error) {
	return f(ctx, limit)
}

func TestNewRetentionValidatesConfiguration(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	valid := purgeFunc(func(context.Context, int) (int, error) { return 0, nil })
	tests := map[string]struct {
		purger   TerminalPurger
		logger   *slog.Logger
		interval time.Duration
		batch    int
	}{
		"purger":     {nil, logger, time.Second, 1},
		"logger":     {valid, nil, time.Second, 1},
		"interval":   {valid, logger, 0, 1},
		"batch low":  {valid, logger, time.Second, 0},
		"batch high": {valid, logger, time.Second, 1001},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRetention(test.purger, test.logger, test.interval, test.batch); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRetentionReportsFailureAndRetriesSerially(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	second := make(chan struct{})
	purger := purgeFunc(func(context.Context, int) (int, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		if calls.Add(1) == 1 {
			return 0, errors.New("database unavailable")
		}
		select {
		case <-second:
		default:
			close(second)
		}
		return 2, nil
	})
	runner, err := NewRetention(purger, logger, time.Millisecond, 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("retention did not retry after failure")
	}
	cancel()
	<-done
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent sweeps = %d, want 1", maximum.Load())
	}
	logs := output.String()
	for _, event := range []string{"maintenance.retention_failed", "maintenance.retention_purged"} {
		if !strings.Contains(logs, event) {
			t.Fatalf("missing %s log: %s", event, logs)
		}
	}
}

func TestRetentionCancellationStopsInflightSweep(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	purger := purgeFunc(func(ctx context.Context, _ int) (int, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return 0, ctx.Err()
	})
	runner, err := NewRetention(purger, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention did not stop after cancellation")
	}
}
