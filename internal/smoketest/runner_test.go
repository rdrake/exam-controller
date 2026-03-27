package smoketest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := CheckHealth(context.Background(), srv.URL)
	if err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestCheckHealth_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := CheckHealth(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error for 503 response")
	}
}

func TestCheckHealth_Unreachable(t *testing.T) {
	err := CheckHealth(context.Background(), "http://127.0.0.1:1")
	if err == nil {
		t.Error("expected error for unreachable host")
	}
}

func TestRunAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []Target{
		{StudentID: "alice", URL: srv.URL},
		{StudentID: "bob", URL: "http://127.0.0.1:1"},
	}

	result := RunAll(context.Background(), targets)
	if result.Passed != 1 {
		t.Errorf("expected 1 passed, got %d", result.Passed)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
	if len(result.Failures) != 1 || result.Failures[0].Student != "bob" {
		t.Errorf("expected bob in failures, got %+v", result.Failures)
	}
}
