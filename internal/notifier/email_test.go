package notifier

import (
	"strings"
	"testing"
)

func TestBuildStudentMessage(t *testing.T) {
	msg := BuildStudentMessage("noreply@test.com", "alice@test.com", "Test Exam", "https://abc123.exam.test.com")
	if !strings.Contains(msg, "From: noreply@test.com") {
		t.Error("missing From header")
	}
	if !strings.Contains(msg, "To: alice@test.com") {
		t.Error("missing To header")
	}
	if !strings.Contains(msg, "https://abc123.exam.test.com") {
		t.Error("missing URL in body")
	}
}

func TestBuildSparesMessage(t *testing.T) {
	urls := []string{"https://x1.exam.test.com", "https://x2.exam.test.com"}
	msg := BuildSparesMessage("noreply@test.com", "prof@test.com", "Test Exam", urls)
	if !strings.Contains(msg, "x1.exam.test.com") {
		t.Error("missing first spare URL")
	}
	if !strings.Contains(msg, "x2.exam.test.com") {
		t.Error("missing second spare URL")
	}
}

func TestBuildUnlockNotification(t *testing.T) {
	failed := []string{"alice"}
	msg := BuildUnlockNotification("noreply@test.com", "prof@test.com", "Test Exam", 48, 2, failed)
	if !strings.Contains(msg, "48 students") {
		t.Error("missing student count")
	}
	if !strings.Contains(msg, "alice") {
		t.Error("missing failed student in notification")
	}
}

func TestBuildLockNotification(t *testing.T) {
	msg := BuildLockNotification("noreply@test.com", "prof@test.com", "Test Exam", 48, 50, 0)
	if !strings.Contains(msg, "ended") {
		t.Error("missing ended message")
	}
}

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

func TestSenderInterfaceCompliance(t *testing.T) {
	var _ Sender = &RetrySender{}
}
