package notifier

import (
	"fmt"
	"net/smtp"
)

// Sender is the interface for sending email.
type Sender interface {
	Send(from string, to []string, msg []byte) error
}

// SMTPSender sends email via SMTP.
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

// FakeSender records sent messages for testing.
type FakeSender struct {
	Sent []SentMessage
}

// SentMessage records a single sent email.
type SentMessage struct {
	From string
	To   []string
	Body []byte
}

func (f *FakeSender) Send(from string, to []string, msg []byte) error {
	f.Sent = append(f.Sent, SentMessage{From: from, To: to, Body: msg})
	return nil
}

// BuildMessage constructs an email message with the exam URL.
func BuildMessage(from, to, subject, url string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nYour exam instance is ready.\r\n\r\nAccess it at: %s\r\n",
		from, to, subject, url)
}
