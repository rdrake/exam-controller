package smoketest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if err := CheckHealth(context.Background(), srv.URL); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckHealth_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	if err := CheckHealth(context.Background(), srv.URL); err == nil {
		t.Error("expected error for 503")
	}
}

func TestCheckBlocked_Reachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	// If we CAN reach it, CheckBlocked should return an error (policy not enforced)
	if err := CheckBlocked(context.Background(), srv.URL); err == nil {
		t.Error("expected error when service is reachable (policy not enforced)")
	}
}

func TestCheckBlocked_Unreachable(t *testing.T) {
	// Unreachable URL — connection refused = policy working
	if err := CheckBlocked(context.Background(), "http://127.0.0.1:1"); err != nil {
		t.Errorf("expected nil (blocked is good), got: %v", err)
	}
}

func TestRunAll(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer bad.Close()

	targets := []Target{
		{StudentID: "alice", URL: good.URL},
		{StudentID: "bob", URL: bad.URL},
	}
	result := RunAll(context.Background(), targets)
	if result.Passed != 1 {
		t.Errorf("passed = %d, want 1", result.Passed)
	}
	if result.Failed != 1 {
		t.Errorf("failed = %d, want 1", result.Failed)
	}
	if len(result.Failures) != 1 || result.Failures[0].Student != "bob" {
		t.Errorf("expected bob in failures")
	}
}

func TestRunDryRun_PolicyEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	targets := []Target{{StudentID: "alice", URL: srv.URL}}
	// negativeURL is unreachable = policy enforced
	dr := RunDryRun(context.Background(), targets, "http://127.0.0.1:1")
	if !dr.PolicyEnforced {
		t.Error("expected PolicyEnforced=true when negative URL is unreachable")
	}
	if dr.Result.Passed != 1 {
		t.Errorf("passed = %d, want 1", dr.Result.Passed)
	}
}

func TestRunDryRun_PolicyNotEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	targets := []Target{{StudentID: "alice", URL: srv.URL}}
	// negativeURL IS reachable = policy NOT enforced
	dr := RunDryRun(context.Background(), targets, srv.URL)
	if dr.PolicyEnforced {
		t.Error("expected PolicyEnforced=false when negative URL is reachable")
	}
}
