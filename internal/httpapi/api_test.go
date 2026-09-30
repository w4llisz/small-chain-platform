package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

func fixture(t *testing.T) (*jobs.Engine, http.Handler) {
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
	if w.Code != 200 || !strings.Contains(w.Body.String(), "small_chain_jobs{state=\"succeeded\"}") || !strings.Contains(w.Body.String(), "small_chain_http_requests_total ") {
		t.Fatal(w.Body.String())
	}
}

func TestReadinessAndAdmissionDuringDrain(t *testing.T) {
	e, h := fixture(t)
	e.StopAdmission()
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
