package smoketest

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Target is a student instance to health-check.
type Target struct {
	StudentID string
	URL       string
}

// Failure records a failed smoke test.
type Failure struct {
	Student string
	Error   string
}

// Result summarizes a smoke test run.
type Result struct {
	Passed   int
	Failed   int
	Failures []Failure
}

// CheckHealth performs an HTTP GET and returns an error if the response is not 2xx.
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

// RunAll checks all targets and returns aggregated results.
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
