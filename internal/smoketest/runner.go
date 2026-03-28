package smoketest

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HealthChecker abstracts health and connectivity checks so that tests can
// inject a fake implementation that avoids real HTTP calls.
type HealthChecker interface {
	CheckHealth(ctx context.Context, url string) error
	CheckBlocked(ctx context.Context, url string) error
}

// HTTPChecker performs real HTTP-based health and blocked checks.
// Zero-value timeouts fall back to sensible defaults (5s for health, 3s for blocked).
type HTTPChecker struct {
	HealthTimeout  time.Duration // default 5s
	BlockedTimeout time.Duration // default 3s
}

func (h *HTTPChecker) healthTimeout() time.Duration {
	if h.HealthTimeout == 0 {
		return 5 * time.Second
	}
	return h.HealthTimeout
}

func (h *HTTPChecker) blockedTimeout() time.Duration {
	if h.BlockedTimeout == 0 {
		return 3 * time.Second
	}
	return h.BlockedTimeout
}

// CheckHealth sends a GET request and returns an error if the response status
// is outside the 2xx range.
func (h *HTTPChecker) CheckHealth(ctx context.Context, url string) error {
	c := &http.Client{Timeout: h.healthTimeout()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// CheckBlocked verifies that a URL is NOT reachable. Returns an error if the
// service IS reachable (meaning NetworkPolicy enforcement is broken).
func (h *HTTPChecker) CheckBlocked(ctx context.Context, url string) error {
	c := &http.Client{Timeout: h.blockedTimeout()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil // Can't even form the request, treat as blocked
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil // Connection refused/timeout = blocked = good
	}
	defer resp.Body.Close()
	return fmt.Errorf("service reachable (HTTP %d) — NetworkPolicy not enforced", resp.StatusCode)
}

// FakeChecker is a test double that returns pre-configured errors.
type FakeChecker struct {
	HealthErr  error
	BlockedErr error
}

func (f *FakeChecker) CheckHealth(_ context.Context, _ string) error  { return f.HealthErr }
func (f *FakeChecker) CheckBlocked(_ context.Context, _ string) error { return f.BlockedErr }

type Target struct {
	StudentID string
	URL       string
}

type Failure struct {
	Student string
	Error   string
}

type Result struct {
	Passed   int
	Failed   int
	Failures []Failure
}

// CheckHealth is a package-level convenience function retained for backwards
// compatibility (e.g. e2e tests). It delegates to a zero-value HTTPChecker.
func CheckHealth(ctx context.Context, url string) error {
	return (&HTTPChecker{}).CheckHealth(ctx, url)
}

// CheckBlocked is a package-level convenience function retained for backwards
// compatibility (e.g. e2e tests). It delegates to a zero-value HTTPChecker.
func CheckBlocked(ctx context.Context, url string) error {
	return (&HTTPChecker{}).CheckBlocked(ctx, url)
}

// RunAll executes health checks against every target using the provided checker.
func RunAll(ctx context.Context, checker HealthChecker, targets []Target) Result {
	var r Result
	for _, t := range targets {
		if err := checker.CheckHealth(ctx, t.URL); err != nil {
			r.Failed++
			r.Failures = append(r.Failures, Failure{Student: t.StudentID, Error: err.Error()})
		} else {
			r.Passed++
		}
	}
	return r
}

// DryRunResult combines health check results with policy enforcement check.
type DryRunResult struct {
	Result         Result
	PolicyEnforced bool
}

// RunDryRun runs all health checks plus a negative connectivity test.
// negativeURL should be a student Service URL reachable only if NetworkPolicy
// enforcement is broken. If CheckBlocked returns an error, PolicyEnforced is false.
func RunDryRun(ctx context.Context, checker HealthChecker, targets []Target, negativeURL string) DryRunResult {
	result := RunAll(ctx, checker, targets)
	policyEnforced := true
	if err := checker.CheckBlocked(ctx, negativeURL); err != nil {
		policyEnforced = false
	}
	return DryRunResult{Result: result, PolicyEnforced: policyEnforced}
}
