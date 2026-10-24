package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckRequiresHTTP200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s", request.Method)
		}
		if request.URL.Path == "/ready" {
			response.WriteHeader(http.StatusOK)
			return
		}
		http.Error(response, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if err := check(context.Background(), server.URL+"/ready"); err != nil {
		t.Fatal(err)
	}
	if err := check(context.Background(), server.URL+"/not-ready"); err == nil || !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Fatalf("non-ready check = %v", err)
	}
}
