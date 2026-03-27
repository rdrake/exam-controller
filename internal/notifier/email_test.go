package notifier

import (
	"strings"
	"testing"
)

func TestBuildMessage(t *testing.T) {
	msg := BuildMessage("noreply@otu.ca", "student@ontariotechu.net", "Your Exam Instance", "https://a3f9b2c1.exam.otu.ca")
	if !strings.Contains(msg, "From: noreply@otu.ca") {
		t.Error("missing From header")
	}
	if !strings.Contains(msg, "To: student@ontariotechu.net") {
		t.Error("missing To header")
	}
	if !strings.Contains(msg, "https://a3f9b2c1.exam.otu.ca") {
		t.Error("missing URL in body")
	}
}

func TestSender_Interface(t *testing.T) {
	var _ Sender = &SMTPSender{}
	var _ Sender = &FakeSender{}
}

func TestFakeSender_Records(t *testing.T) {
	f := &FakeSender{}
	err := f.Send("noreply@otu.ca", []string{"student@test.com"}, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(f.Sent))
	}
}
