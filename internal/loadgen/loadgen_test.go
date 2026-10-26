package loadgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBoundsConcurrencyUsesUniqueKeysAndClassifies(t *testing.T) {
	var current atomic.Int64
	var maximum atomic.Int64
	var keysMu sync.Mutex
	keys := make([]string, 0, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Add(1)
		defer current.Add(-1)
		for {
			old := maximum.Load()
			if active <= old || maximum.CompareAndSwap(old, active) {
				break
			}
		}
		keysMu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		keysMu.Unlock()
		time.Sleep(10 * time.Millisecond)

		var index int
		if _, err := fmt.Sscanf(r.Header.Get("Idempotency-Key"), "load-%d", &index); err != nil {
			t.Errorf("parse key: %v", err)
		}
		switch index % 5 {
		case 1:
			w.WriteHeader(http.StatusAccepted)
		case 2:
			w.WriteHeader(http.StatusOK)
		case 3:
			w.WriteHeader(http.StatusTooManyRequests)
		case 4:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"record_capacity"}}`)
		case 0:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	config := validConfig(server.URL)
	config.Requests = 10
	config.Concurrency = 3
	summary, err := Run(context.Background(), server.Client(), config)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := maximum.Load(); got != 3 {
		t.Fatalf("maximum concurrency = %d, want 3", got)
	}
	wantCounts := Counts{Offered: 10, Accepted: 2, Replayed: 2, QueueFull: 2, RecordCapacity: 2, ServerError: 2}
	if summary.Counts != wantCounts {
		t.Fatalf("counts = %+v, want %+v", summary.Counts, wantCounts)
	}
	sort.Strings(keys)
	for i, key := range keys {
		want := fmt.Sprintf("load-%06d", i+1)
		if key != want {
			t.Fatalf("key[%d] = %q, want %q", i, key, want)
		}
	}
	if summary.Latency.Buckets[len(summary.Latency.Buckets)-1].Count != 10 {
		t.Fatalf("+Inf bucket = %d, want 10", summary.Latency.Buckets[len(summary.Latency.Buckets)-1].Count)
	}
	for i := 1; i < len(summary.Latency.Buckets); i++ {
		if summary.Latency.Buckets[i].Count < summary.Latency.Buckets[i-1].Count {
			t.Fatalf("buckets are not cumulative: %+v", summary.Latency.Buckets)
		}
	}
	if summary.Workload.PayloadSHA256 == "" || summary.SchemaVersion != SchemaVersion {
		t.Fatalf("summary metadata is incomplete: %+v", summary)
	}
}

func TestRunCountsTransportErrorsWithoutRetrying(t *testing.T) {
	config := validConfig("http://example.test")
	config.Requests = 4
	config.Concurrency = 2
	var calls atomic.Int64
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("connection reset")
	})

	summary, err := Run(context.Background(), doer, config)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Counts.Offered != 4 || summary.Counts.TransportError != 4 {
		t.Fatalf("counts = %+v", summary.Counts)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("HTTP calls = %d, want 4", got)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "path", change: func(c *Config) { c.BaseURL += "/v1" }},
		{name: "requests", change: func(c *Config) { c.Requests = 0 }},
		{name: "concurrency", change: func(c *Config) { c.Concurrency = c.Requests + 1 }},
		{name: "timeout", change: func(c *Config) { c.RequestTimeout = time.Minute + time.Millisecond }},
		{name: "key", change: func(c *Config) { c.KeyPrefix = "bad key" }},
		{name: "payload", change: func(c *Config) { c.Payload = string([]byte{0xff}) }},
		{name: "delay", change: func(c *Config) { c.DelayMS = 5001 }},
		{name: "failures", change: func(c *Config) { c.FailFirstAttempts = 6 }},
		{name: "attempts", change: func(c *Config) { c.MaxAttempts = 0 }},
		{name: "task timeout", change: func(c *Config) { c.TimeoutMS = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig("http://127.0.0.1:8080")
			test.change(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func validConfig(baseURL string) Config {
	return Config{
		BaseURL:        baseURL,
		Requests:       1,
		Concurrency:    1,
		RequestTimeout: time.Second,
		KeyPrefix:      "load",
		Payload:        "fixed",
		MaxAttempts:    3,
		TimeoutMS:      1000,
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}
