package notifier

import (
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// Sender sends email messages.
type Sender interface {
	Send(from string, to []string, msg []byte) error
}

// SMTPSender sends emails via SMTP.
type SMTPSender struct {
	Host     string
	Port     int
	Username string
	Password string
}

func (s *SMTPSender) Send(from string, to []string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
	return smtp.SendMail(addr, auth, from, to, msg)
}

// SentMessage records a sent message for testing.
type SentMessage struct {
	From string
	To   []string
	Body []byte
}

// FakeSender records messages for testing.
type FakeSender struct {
	Sent []SentMessage
}

func (f *FakeSender) Send(from string, to []string, msg []byte) error {
	f.Sent = append(f.Sent, SentMessage{From: from, To: to, Body: msg})
	return nil
}

// RetrySender wraps a Sender with exponential backoff retries.
type RetrySender struct {
	inner      Sender
	maxRetries int
	SleepFunc  func(time.Duration) // injectable; nil defaults to time.Sleep
}

func NewRetrySender(inner Sender, maxRetries int) *RetrySender {
	return &RetrySender{inner: inner, maxRetries: maxRetries}
}

func (r *RetrySender) Send(from string, to []string, msg []byte) error {
	sleep := r.SleepFunc
	if sleep == nil {
		sleep = time.Sleep
	}
	var err error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		err = r.inner.Send(from, to, msg)
		if err == nil {
			return nil
		}
		if attempt < r.maxRetries {
			sleep(time.Duration(1<<uint(attempt)) * 100 * time.Millisecond)
		}
	}
	return fmt.Errorf("after %d retries: %w", r.maxRetries, err)
}

// FailNSender fails the first N attempts, then succeeds. For testing.
type FailNSender struct {
	FailCount int
	Attempts  int
}

func (f *FailNSender) Send(from string, to []string, msg []byte) error {
	f.Attempts++
	if f.Attempts <= f.FailCount {
		return fmt.Errorf("simulated failure %d", f.Attempts)
	}
	return nil
}

func buildMessage(from, to, subject, body string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", from, to, subject, body)
}

func BuildStudentMessage(from, to, subject, url string) string {
	body := fmt.Sprintf("Your exam instance is ready.\n\nAccess your instance at: %s\n\nThis link is unique to you. Do not share it.\n", url)
	return buildMessage(from, to, subject, body)
}

func BuildSparesMessage(from, to, subject string, urls []string) string {
	body := fmt.Sprintf("Spare instances are ready.\n\n%s\n", strings.Join(urls, "\n"))
	return buildMessage(from, to, subject+" - Spare Instances", body)
}

func BuildUnlockNotification(from, to, subject string, students, spares int, failedEmails []string) string {
	body := fmt.Sprintf("Exam is live.\n\n%d students, %d spares.\n", students, spares)
	if len(failedEmails) > 0 {
		body += fmt.Sprintf("\nFailed email delivery for: %s\nPlease share URLs manually.\n", strings.Join(failedEmails, ", "))
	}
	return buildMessage(from, to, subject+" - Exam Unlocked", body)
}

func BuildLockNotification(from, to, subject string, students, healthy, failed int) string {
	body := fmt.Sprintf("Exam has ended.\n\n%d students, %d instances healthy, %d failed.\n", students, healthy, failed)
	return buildMessage(from, to, subject+" - Exam Locked", body)
}
