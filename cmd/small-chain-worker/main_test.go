package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/w4llisz/small-chain-platform/internal/dbworker"
)

func TestRunValidatesArgumentsBeforeConnecting(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "owner", want: "owner is required"},
		{name: "invalid owner", args: []string{"-owner=bad owner"}, want: "worker owner"},
		{name: "database URL", args: []string{"-owner=test-worker"}, want: "DATABASE_URL is required"},
		{name: "positionals", args: []string{"unexpected"}, want: "unexpected positional arguments"},
		{name: "unknown flag", args: []string{"-unknown"}, want: "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args, logger)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run(%q) = %v, want error containing %q", test.args, err, test.want)
			}
		})
	}
}

func TestMetricsHandlerExposesOnlyMetricsGET(t *testing.T) {
	handler := metricsHandler(&dbworker.Worker{})
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(response.Body.String(), "small_chain_worker_attempt_duration_seconds_count 0\n") {
		t.Fatalf("unexpected metrics:\n%s", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/metrics", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics status = %d, want 405", response.Code)
	}
}
