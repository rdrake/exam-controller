package notifier

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Message builder tests
// ---------------------------------------------------------------------------

func TestBuildStudentMessage(t *testing.T) {
	msg := BuildStudentMessage("noreply@test.com", "alice@test.com", "Test Exam", "https://abc123.exam.test.com")

	checks := map[string]string{
		"From header":             "From: noreply@test.com",
		"To header":               "To: alice@test.com",
		"Subject header":          "Subject: Test Exam",
		"MIME-Version header":     "MIME-Version: 1.0",
		"Content-Type header":     "Content-Type: text/plain; charset=UTF-8",
		"URL in body":             "https://abc123.exam.test.com",
		"exam instance ready msg": "Your exam instance is ready",
	}
	for label, want := range checks {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %s: want substring %q", label, want)
		}
	}
}

func TestBuildStudentMessage_SpecialCharsInEmail(t *testing.T) {
	addr := "student+test@example.com"
	msg := BuildStudentMessage("noreply@test.com", addr, "Exam", "https://instance.exam.test.com")

	if !strings.Contains(msg, "To: "+addr) {
		t.Errorf("special-char email not in To header; got:\n%s", msg)
	}
	if !strings.Contains(msg, "https://instance.exam.test.com") {
		t.Error("missing URL in body")
	}
}

func TestBuildSparesMessage(t *testing.T) {
	urls := []string{"https://x1.exam.test.com", "https://x2.exam.test.com"}
	msg := BuildSparesMessage("noreply@test.com", "prof@test.com", "Test Exam", urls)

	if !strings.Contains(msg, "https://x1.exam.test.com") {
		t.Error("missing first spare URL")
	}
	if !strings.Contains(msg, "https://x2.exam.test.com") {
		t.Error("missing second spare URL")
	}
	if !strings.Contains(msg, "Subject: Test Exam - Spare Instances") {
		t.Error("subject missing 'Spare Instances' suffix")
	}
}

func TestBuildSparesMessage_NoSpares(t *testing.T) {
	msg := BuildSparesMessage("noreply@test.com", "prof@test.com", "Test Exam", []string{})

	// Should still produce a structurally valid message with headers.
	if !strings.Contains(msg, "From: noreply@test.com") {
		t.Error("missing From header")
	}
	if !strings.Contains(msg, "Subject: Test Exam - Spare Instances") {
		t.Error("missing Subject header")
	}
	if !strings.Contains(msg, "MIME-Version: 1.0") {
		t.Error("missing MIME-Version header")
	}
}

func TestBuildUnlockNotification(t *testing.T) {
	msg := BuildUnlockNotification("noreply@test.com", "prof@test.com", "Test Exam", 48, 2, nil)

	if !strings.Contains(msg, "48 students") {
		t.Error("missing student count")
	}
	if !strings.Contains(msg, "2 spares") {
		t.Error("missing spare count")
	}
	if !strings.Contains(msg, "Subject: Test Exam - Exam Unlocked") {
		t.Error("subject missing 'Exam Unlocked' suffix")
	}
}

func TestBuildUnlockNotification_WithFailedEmails(t *testing.T) {
	failed := []string{"alice", "bob"}
	msg := BuildUnlockNotification("noreply@test.com", "prof@test.com", "Test Exam", 48, 2, failed)

	if !strings.Contains(msg, "alice") {
		t.Error("missing failed email ID 'alice'")
	}
	if !strings.Contains(msg, "bob") {
		t.Error("missing failed email ID 'bob'")
	}
	if !strings.Contains(msg, "Failed email delivery") {
		t.Error("missing failed delivery notice")
	}
}

func TestBuildLockNotification(t *testing.T) {
	msg := BuildLockNotification("noreply@test.com", "prof@test.com", "Test Exam", 48, 50, 3)

	if !strings.Contains(msg, "48 students") {
		t.Error("missing student count")
	}
	if !strings.Contains(msg, "50 instances healthy") {
		t.Error("missing healthy count")
	}
	if !strings.Contains(msg, "3 failed") {
		t.Error("missing failed count")
	}
	if !strings.Contains(msg, "Subject: Test Exam - Exam Locked") {
		t.Error("subject missing 'Exam Locked' suffix")
	}
}

// ---------------------------------------------------------------------------
// FakeSender / RetrySender tests
// ---------------------------------------------------------------------------

func TestFakeSenderRecords(t *testing.T) {
	s := &FakeSender{}
	err := s.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(s.Sent))
	}
}

func TestRetrySender_SucceedsFirstTry(t *testing.T) {
	inner := &FakeSender{}
	rs := NewRetrySender(inner, 3)
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.Sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(inner.Sent))
	}
}

func TestRetrySender_RetriesOnFailure(t *testing.T) {
	inner := &FailNSender{FailCount: 2}
	rs := NewRetrySender(inner, 3)
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if inner.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", inner.Attempts)
	}
}

func TestRetrySender_ExhaustsRetries(t *testing.T) {
	inner := &FailNSender{FailCount: 5}
	rs := NewRetrySender(inner, 3)
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestRetrySender_BackoffTiming(t *testing.T) {
	var sleeps []time.Duration
	rs := NewRetrySender(&FailNSender{FailCount: 2}, 3)
	rs.SleepFunc = func(d time.Duration) { sleeps = append(sleeps, d) }
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("test"))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(sleeps) != 2 {
		t.Fatalf("expected 2 sleeps, got %d", len(sleeps))
	}
	// 100ms * 2^0 = 100ms, 100ms * 2^1 = 200ms
	if sleeps[0] != 100*time.Millisecond {
		t.Errorf("first backoff = %v, want 100ms", sleeps[0])
	}
	if sleeps[1] != 200*time.Millisecond {
		t.Errorf("second backoff = %v, want 200ms", sleeps[1])
	}
}

func TestRetrySender_ZeroRetries(t *testing.T) {
	inner := &FailNSender{FailCount: 1}
	rs := NewRetrySender(inner, 0)
	rs.SleepFunc = func(d time.Duration) { t.Fatal("sleep should not be called with zero retries") }
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err == nil {
		t.Fatal("expected error with zero retries and a failing sender")
	}
	if !strings.Contains(err.Error(), "after 0 retries") {
		t.Errorf("error message = %q, want it to contain 'after 0 retries'", err.Error())
	}
	if inner.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (single attempt with 0 retries)", inner.Attempts)
	}
}

func TestRetrySender_AllRetriesFail(t *testing.T) {
	inner := &FailNSender{FailCount: 10}
	rs := NewRetrySender(inner, 3)
	rs.SleepFunc = func(d time.Duration) {} // no-op to avoid real sleep
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err == nil {
		t.Fatal("expected error when all retries fail")
	}
	// Verify the error wraps the inner sender's error.
	if !strings.Contains(err.Error(), "simulated failure") {
		t.Errorf("error should wrap inner error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "after 3 retries") {
		t.Errorf("error should mention retry count, got: %v", err)
	}
}

func TestRetrySender_NoSleepAfterLastAttempt(t *testing.T) {
	var sleepCount int
	inner := &FailNSender{FailCount: 3} // all 3 attempts (0,1,2) fail
	rs := NewRetrySender(inner, 2)
	rs.SleepFunc = func(d time.Duration) { sleepCount++ }
	_ = rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	// With maxRetries=2 the loop runs attempts 0, 1, 2 (3 total).
	// Sleep happens after attempt 0 and 1, but NOT after attempt 2 (the last).
	if sleepCount != 2 {
		t.Errorf("sleepCount = %d, want 2 (no sleep after last failed attempt)", sleepCount)
	}
}

// ---------------------------------------------------------------------------
// FailNSender tests
// ---------------------------------------------------------------------------

func TestFailNSender_TracksAttempts(t *testing.T) {
	s := &FailNSender{FailCount: 2}
	errors := make([]error, 0, 5)
	for range 5 {
		errors = append(errors, s.Send("f@t.com", []string{"t@t.com"}, []byte("msg")))
	}
	if s.Attempts != 5 {
		t.Fatalf("Attempts = %d, want 5", s.Attempts)
	}
	// First 2 calls should fail.
	for i := range 2 {
		if errors[i] == nil {
			t.Errorf("call %d: expected error, got nil", i+1)
		}
	}
	// Remaining 3 calls should succeed.
	for i := 2; i < 5; i++ {
		if errors[i] != nil {
			t.Errorf("call %d: expected nil, got %v", i+1, errors[i])
		}
	}
}

func TestSenderInterfaceCompliance(t *testing.T) {
	var _ Sender = &RetrySender{}
}
