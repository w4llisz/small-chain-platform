// Package jobs implements a bounded, single-process task execution engine.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid task")
	ErrConflict = errors.New("idempotency key already used with a different request")
	ErrFull     = errors.New("queue is full")
	ErrCapacity = errors.New("record capacity reached")
	ErrClosed   = errors.New("engine is draining")
	ErrNotFound = errors.New("job not found")
	ErrTerminal = errors.New("job already finished")
)

type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Retrying  State = "retrying"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Canceled  State = "canceled"
)

func (s State) Terminal() bool { return s == Succeeded || s == Failed || s == Canceled }

// Spec is deliberately comparable: idempotency compares normalized requests.
// DelayMS and FailFirstAttempts are explicit demo fault-injection controls.
type Spec struct {
	Kind              string `json:"kind"`
	Payload           string `json:"payload"`
	DelayMS           int    `json:"delay_ms,omitempty"`
	FailFirstAttempts int    `json:"fail_first_attempts,omitempty"`
	MaxAttempts       int    `json:"max_attempts,omitempty"`
	TimeoutMS         int    `json:"timeout_ms,omitempty"`
}

func (s Spec) Normalize() (Spec, error) {
	if s.MaxAttempts == 0 {
		s.MaxAttempts = 3
	}
	if s.TimeoutMS == 0 {
		s.TimeoutMS = 1000
	}
	if s.Kind != "demo.checksum" || len(s.Payload) > 4096 || s.DelayMS < 0 || s.DelayMS > 5000 || s.FailFirstAttempts < 0 || s.FailFirstAttempts > 5 || s.MaxAttempts < 1 || s.MaxAttempts > 5 || s.TimeoutMS < 1 || s.TimeoutMS > 30000 {
		return Spec{}, fmt.Errorf("%w: kind=demo.checksum; payload<=4096 bytes; delay_ms=0..5000; fail_first_attempts=0..5; max_attempts=1..5; timeout_ms=1..30000", ErrInvalid)
	}
	return s, nil
}

type Job struct {
	ID        string    `json:"id"`
	Spec      Spec      `json:"spec"`
	State     State     `json:"state"`
	Attempts  int       `json:"attempts"`
	Result    string    `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Executor implementations must honor ctx and must be safe for concurrent use.
// Retried side effects must be idempotent; attempt is one-based.
type Executor func(ctx context.Context, spec Spec, attempt int) (string, error)

type temporaryError struct{ err error }

func (e temporaryError) Error() string { return e.err.Error() }
func (e temporaryError) Unwrap() error { return e.err }
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return temporaryError{err: err}
}

func isRetryable(err error) bool {
	var temporary temporaryError
	return errors.As(err, &temporary)
}

func Checksum(ctx context.Context, spec Spec, attempt int) (string, error) {
	timer := time.NewTimer(time.Duration(spec.DelayMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if attempt <= spec.FailFirstAttempts {
		return "", Retryable(errors.New("injected transient failure"))
	}
	sum := sha256.Sum256([]byte(spec.Payload))
	return hex.EncodeToString(sum[:]), nil
}
