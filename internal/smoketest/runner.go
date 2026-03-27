package smoketest

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

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

func CheckHealth(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
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
func CheckBlocked(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil // Can't even form the request, treat as blocked
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil // Connection refused/timeout = blocked = good
	}
	defer resp.Body.Close()
	return fmt.Errorf("service reachable (HTTP %d) — NetworkPolicy not enforced", resp.StatusCode)
}

func RunAll(ctx context.Context, targets []Target) Result {
	var r Result
	for _, t := range targets {
		if err := CheckHealth(ctx, t.URL); err != nil {
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
func RunDryRun(ctx context.Context, targets []Target, negativeURL string) DryRunResult {
	result := RunAll(ctx, targets)
	policyEnforced := true
	if err := CheckBlocked(ctx, negativeURL); err != nil {
		policyEnforced = false
	}
	return DryRunResult{Result: result, PolicyEnforced: policyEnforced}
}
