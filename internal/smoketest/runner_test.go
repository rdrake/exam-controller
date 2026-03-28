package smoketest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	result := RunAll(context.Background(), &HTTPChecker{}, targets)
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
	dr := RunDryRun(context.Background(), &HTTPChecker{}, targets, "http://127.0.0.1:1")
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
	dr := RunDryRun(context.Background(), &HTTPChecker{}, targets, srv.URL)
	if dr.PolicyEnforced {
		t.Error("expected PolicyEnforced=false when negative URL is reachable")
	}
}

// --- HTTPChecker tests ---

func TestHTTPChecker_CheckHealth_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	checker := &HTTPChecker{HealthTimeout: 50 * time.Millisecond}
	err := checker.CheckHealth(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// The error message should indicate a timeout or deadline exceeded.
	msg := err.Error()
	if !strings.Contains(msg, "Timeout") && !strings.Contains(msg, "timeout") &&
		!strings.Contains(msg, "deadline") && !strings.Contains(msg, "Deadline") {
		t.Errorf("expected timeout-related error, got: %v", err)
	}
}

func TestHTTPChecker_CheckHealth_StatusCodes(t *testing.T) {
	tests := []struct {
		code    int
		wantErr bool
	}{
		{200, false},
		{201, false},
		{299, false},
		{300, true},
		{400, true},
		{500, true},
	}

	for _, tc := range tests {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()

			checker := &HTTPChecker{}
			err := checker.CheckHealth(context.Background(), srv.URL)
			if tc.wantErr && err == nil {
				t.Errorf("status %d: expected error, got nil", tc.code)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("status %d: unexpected error: %v", tc.code, err)
			}
		})
	}
}

func TestHTTPChecker_CheckBlocked_ConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	// Immediately close so the port is no longer listening.
	srv.Close()

	checker := &HTTPChecker{}
	err := checker.CheckBlocked(context.Background(), srv.URL)
	if err != nil {
		t.Errorf("expected nil (connection refused = blocked = good), got: %v", err)
	}
}

func TestHTTPChecker_CheckBlocked_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	checker := &HTTPChecker{BlockedTimeout: 50 * time.Millisecond}
	err := checker.CheckBlocked(context.Background(), srv.URL)
	if err != nil {
		t.Errorf("expected nil (timeout = blocked = good), got: %v", err)
	}
}

// --- FakeChecker tests ---

func TestRunAll_FakeChecker_AllHealthy(t *testing.T) {
	checker := &FakeChecker{HealthErr: nil}
	targets := []Target{
		{StudentID: "alice", URL: "http://fake1"},
		{StudentID: "bob", URL: "http://fake2"},
		{StudentID: "carol", URL: "http://fake3"},
	}
	result := RunAll(context.Background(), checker, targets)
	if result.Passed != 3 {
		t.Errorf("Passed = %d, want 3", result.Passed)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
}

func TestRunAll_FakeChecker_AllFailed(t *testing.T) {
	checker := &FakeChecker{HealthErr: errors.New("down")}
	targets := []Target{
		{StudentID: "alice", URL: "http://fake1"},
		{StudentID: "bob", URL: "http://fake2"},
		{StudentID: "carol", URL: "http://fake3"},
	}
	result := RunAll(context.Background(), checker, targets)
	if result.Passed != 0 {
		t.Errorf("Passed = %d, want 0", result.Passed)
	}
	if result.Failed != 3 {
		t.Errorf("Failed = %d, want 3", result.Failed)
	}
}

func TestRunAll_EmptyTargets(t *testing.T) {
	checker := &FakeChecker{}
	result := RunAll(context.Background(), checker, []Target{})
	if result.Passed != 0 {
		t.Errorf("Passed = %d, want 0", result.Passed)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
	if len(result.Failures) != 0 {
		t.Errorf("Failures = %v, want empty", result.Failures)
	}
}

func TestRunDryRun_FakeChecker_PolicyEnforced(t *testing.T) {
	checker := &FakeChecker{HealthErr: nil, BlockedErr: nil}
	targets := []Target{{StudentID: "alice", URL: "http://fake1"}}
	dr := RunDryRun(context.Background(), checker, targets, "http://negative")
	if !dr.PolicyEnforced {
		t.Error("expected PolicyEnforced=true when BlockedErr is nil")
	}
}

func TestRunDryRun_FakeChecker_PolicyNotEnforced(t *testing.T) {
	checker := &FakeChecker{BlockedErr: errors.New("reachable")}
	targets := []Target{{StudentID: "alice", URL: "http://fake1"}}
	dr := RunDryRun(context.Background(), checker, targets, "http://negative")
	if dr.PolicyEnforced {
		t.Error("expected PolicyEnforced=false when BlockedErr is non-nil")
	}
}
