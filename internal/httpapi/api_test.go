package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func fixture(t *testing.T) (*jobs.Engine, *API) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e, err := jobs.New(jobs.Config{Workers: 1, QueueCapacity: 8, MaxJobs: 100, RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond}, jobs.Checksum, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := e.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return e, New(e, logger)
}

func request(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPContract(t *testing.T) {
	_, h := fixture(t)
	body := `{"kind":"demo.checksum","payload":"hello"}`
	w := request(h, "POST", "/v1/jobs", "one", body)
	if w.Code != 202 || w.Header().Get("Location") == "" || w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	w = request(h, "POST", "/v1/jobs", "one", body)
	if w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d", w.Code)
	}
	for _, tc := range []struct {
		method, path, key, body string
		status                  int
	}{
		{"GET", location, "", "", 200},
		{"GET", "/v1/jobs", "", "", 501},
		{"GET", "/v1/jobs?state=unknown", "", "", 400},
		{"GET", "/v1/jobs?limit=101", "", "", 400},
		{"GET", "/v1/jobs?cursor=not-base64url!", "", "", 400},
		{"GET", location + "/events", "", "", 501},
		{"GET", location + "/events?cursor=-1", "", "", 400},
		{"GET", location + "/events?limit=101", "", "", 400},
		{"POST", "/v1/jobs", "one", `{"kind":"demo.checksum","payload":"changed"}`, 409},
		{"POST", "/v1/jobs", "", body, 400},
		{"POST", "/v1/jobs", "bad", "{", 400},
		{"POST", "/v1/jobs", "unknown", `{"kind":"demo.checksum","unknown":true}`, 400},
		{"POST", "/v1/jobs", "two", body + body, 400},
		{"POST", "/v1/jobs", "null", "null", 400},
		{"POST", "/v1/jobs", "large", `{"payload":"` + strings.Repeat("a", 17<<10) + `"}`, 413},
		{"GET", "/v1/jobs/missing", "", "", 404},
		{"POST", "/v1/jobs/missing/cancel", "", "", 404},
		{"DELETE", location, "", "", 405},
		{"GET", "/healthz", "", "", 200},
		{"GET", "/readyz", "", "", 200},
	} {
		t.Run(tc.method+tc.path+tc.key, func(t *testing.T) {
			w := request(h, tc.method, tc.path, tc.key, tc.body)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	w = request(h, "GET", "/metrics", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "small_chain_job_state_metrics_up 1") || !strings.Contains(w.Body.String(), "small_chain_jobs{state=\"succeeded\"}") || !strings.Contains(w.Body.String(), "small_chain_http_requests_total ") {
		t.Fatal(w.Body.String())
	}
}

func TestMetricsExposeStateReadFailureWithoutStaleGauges(t *testing.T) {
	engine, _ := fixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	failedRead := func(context.Context) (postgres.JobCounts, error) { return nil, context.DeadlineExceeded }
	api := newAPI(memoryBackend{engine: engine}, nil, failedRead, "postgres", logger)
	w := request(api, "GET", "/metrics", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "small_chain_job_state_metrics_up 0") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "small_chain_jobs{") {
		t.Fatalf("failed scrape exposed stale state gauges: %s", w.Body.String())
	}
}

func TestJobListCursorBindsStateFilter(t *testing.T) {
	position := postgres.JobPosition{CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ID: strings.Repeat("a", 32)}
	raw, err := encodeJobListCursor(position, jobs.Queued)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeJobListCursor(raw, jobs.Queued)
	if err != nil || decoded.ID != position.ID || !decoded.CreatedAt.Equal(position.CreatedAt) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if _, err := decodeJobListCursor(raw, jobs.Failed); err == nil {
		t.Fatal("cursor accepted with a different state filter")
	}
	for _, invalid := range []string{"not-base64url!", strings.Repeat("a", maxJobCursorLength+1)} {
		if _, err := decodeJobListCursor(invalid, ""); err == nil {
			t.Fatalf("invalid cursor accepted: %q", invalid)
		}
	}
}

func TestReadinessAndAdmissionDuringDrain(t *testing.T) {
	_, h := fixture(t)
	h.StopAdmission()
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/healthz", "", 200}, {"GET", "/readyz", "", 503}, {"POST", "/v1/jobs", `{"kind":"demo.checksum"}`, 503},
	} {
		w := request(h, tc.method, tc.path, "new", tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}

// A real TCP server and HTTP client exercise JSON, retries, and result polling.
func TestEndToEndRetryAndResult(t *testing.T) {
	_, h := fixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Second
	r, err := http.NewRequest("POST", server.URL+"/v1/jobs", strings.NewReader(`{"kind":"demo.checksum","payload":"hello","fail_first_attempts":2}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Idempotency-Key", "e2e")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	location := resp.Header.Get("Location")
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatal(resp.Status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err = client.Get(server.URL + location)
		if err != nil {
			t.Fatal(err)
		}
		var j jobs.Job
		err = json.NewDecoder(resp.Body).Decode(&j)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if j.State.Terminal() {
			if j.State != jobs.Succeeded || j.Attempts != 3 || j.Result != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
				t.Fatalf("%+v", j)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("E2E timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCancelEndpoint(t *testing.T) {
	_, h := fixture(t)
	w := request(h, "POST", "/v1/jobs", "cancel", `{"kind":"demo.checksum","delay_ms":5000,"timeout_ms":10000}`)
	path := w.Header().Get("Location") + "/cancel"
	for i := 0; i < 2; i++ {
		w = request(h, "POST", path, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"canceled"`) {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
	}
}

func TestAdmissionTraceUsesBoundedCorrelationAttributes(t *testing.T) {
	engine, _ := fixture(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	api := New(engine, slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithTracing(provider.Tracer("test"), propagation.TraceContext{}))

	r := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"kind":"demo.checksum","payload":"do-not-export"}`))
	r.Header.Set("Idempotency-Key", "do-not-export-key")
	r.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans=%d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != "small_chain.http.admission" || span.SpanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("unexpected span: name=%q trace=%s", span.Name, span.SpanContext.TraceID())
	}
	attributes := traceAttributes(span.Attributes)
	if attributes["small_chain.admission.outcome"] != "accepted" ||
		attributes["small_chain.idempotency.replayed"] != "false" ||
		len(attributes["small_chain.job.id"]) != 32 {
		t.Fatalf("unexpected attributes: %#v", attributes)
	}
	for key, value := range attributes {
		if strings.Contains(key, "payload") || strings.Contains(key, "idempotency_key") ||
			strings.Contains(value, "do-not-export") {
			t.Fatalf("sensitive or unbounded attribute exported: %s=%q", key, value)
		}
	}
}

type failingTraceExporter struct{}

func (failingTraceExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("collector unavailable")
}
func (failingTraceExporter) Shutdown(context.Context) error { return nil }

func TestAdmissionSucceedsWhenTraceExportFails(t *testing.T) {
	engine, _ := fixture(t)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(failingTraceExporter{}), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	api := New(engine, slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithTracing(provider.Tracer("test"), propagation.TraceContext{}))

	w := request(api, http.MethodPost, "/v1/jobs", "export-failure", `{"kind":"demo.checksum"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("trace export failure changed admission: status=%d body=%s", w.Code, w.Body.String())
	}
}

func traceAttributes(values []attribute.KeyValue) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		result[string(value.Key)] = value.Value.Emit()
	}
	return result
}
