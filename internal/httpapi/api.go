// Package httpapi exposes the task lifecycle without owning engine state.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

type API struct {
	engine       *jobs.Engine
	log          *slog.Logger
	requests     atomic.Uint64
	serverErrors atomic.Uint64
	sequence     atomic.Uint64
}

func New(engine *jobs.Engine, logger *slog.Logger) http.Handler {
	a := &API{engine: engine, log: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", a.submit)
	mux.HandleFunc("GET /v1/jobs/{id}", a.get)
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", a.cancel)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !engine.Stats().Accepting {
			writeError(w, http.StatusServiceUnavailable, "draining", "engine is draining")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /metrics", a.metrics)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Generate locally; never trust arbitrary client header values in logs.
		id := strconv.FormatUint(a.sequence.Add(1), 10)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(rw, r)
		a.requests.Add(1)
		if rw.status >= 500 {
			a.serverErrors.Add(1)
		}
		a.log.Info("http.request", "request_id", id, "method", r.Method, "route", r.Pattern, "status", rw.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var spec jobs.Spec
	if err := dec.Decode(&spec); err != nil {
		decodeError(w, err)
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		decodeError(w, err)
		return
	}
	j, replay, err := a.engine.Submit(r.Header.Get("Idempotency-Key"), spec)
	if err != nil {
		a.engineError(w, err)
		return
	}
	w.Header().Set("Location", "/v1/jobs/"+j.ID)
	w.Header().Set("Idempotency-Replayed", strconv.FormatBool(replay))
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	}
	writeJSON(w, status, j)
}

func decodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 16 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "expected one JSON object with known fields")
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	j, err := a.engine.Get(r.PathValue("id"))
	if err != nil {
		a.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	j, err := a.engine.Cancel(r.PathValue("id"))
	if err != nil {
		a.engineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (a *API) engineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobs.ErrInvalid):
		writeError(w, 400, "invalid_request", err.Error())
	case errors.Is(err, jobs.ErrNotFound):
		writeError(w, 404, "not_found", err.Error())
	case errors.Is(err, jobs.ErrConflict), errors.Is(err, jobs.ErrTerminal):
		writeError(w, 409, "conflict", err.Error())
	case errors.Is(err, jobs.ErrFull):
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "queue_full", err.Error())
	case errors.Is(err, jobs.ErrClosed):
		writeError(w, 503, "draining", err.Error())
	case errors.Is(err, jobs.ErrCapacity):
		writeError(w, 503, "record_capacity", err.Error())
	default:
		a.log.Error("http.internal_error", "error", err)
		writeError(w, 500, "internal_error", "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	s := a.engine.Stats()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	metric := func(name, typ, help string, value any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, value)
	}
	metric("small_chain_queue_depth", "gauge", "Pending channel entries including canceled tombstones.", s.QueueDepth)
	metric("small_chain_workers", "gauge", "Configured worker slots, including slots occupied by retry backoff.", s.Workers)
	metric("small_chain_record_capacity", "gauge", "Maximum retained job records.", s.MaxJobs)
	metric("small_chain_attempts_total", "counter", "Started execution attempts.", s.Attempts)
	metric("small_chain_retries_total", "counter", "Scheduled retries.", s.Retries)
	metric("small_chain_rejections_total", "counter", "Admission rejections for queue, retention capacity or shutdown.", s.Rejected)
	metric("small_chain_http_requests_total", "counter", "Completed HTTP requests including probes.", a.requests.Load())
	metric("small_chain_http_server_errors_total", "counter", "HTTP responses with 5xx status.", a.serverErrors.Load())
	fmt.Fprintln(w, "# HELP small_chain_jobs Retained jobs by lifecycle state.\n# TYPE small_chain_jobs gauge")
	for _, state := range []jobs.State{jobs.Queued, jobs.Running, jobs.Retrying, jobs.Succeeded, jobs.Failed, jobs.Canceled} {
		fmt.Fprintf(w, "small_chain_jobs{state=%q} %d\n", state, s.States[state])
	}
	fmt.Fprintln(w, "# HELP small_chain_attempt_duration_seconds Execution attempt duration, excluding backoff; no quantiles.\n# TYPE small_chain_attempt_duration_seconds summary")
	fmt.Fprintf(w, "small_chain_attempt_duration_seconds_sum %g\nsmall_chain_attempt_duration_seconds_count %d\n", s.DurationSeconds, s.Completed)
}
