package mailer

import (
	"fmt"
	"log/slog"
	"net/smtp"
	"os"
)

type Mailer struct {
	from string
	host string
	port string
	auth smtp.Auth
}

// New builds a Mailer from SMTP env vars (host/port/user/pass/from).
func New() *Mailer {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	from := os.Getenv("SMTP_FROM")
	if from == "" {
		from = user
	}

	var auth smtp.Auth
	if user != "" && pass != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}

	return &Mailer{from: from, host: host, port: port, auth: auth}
}

// Send sends a plain-text email. If SMTP_HOST is not set, the email is logged
// to stdout instead (dev mode).
func (m *Mailer) Send(to, subject, body string) error {
	if m.host == "" {
		slog.Info("mailer dev mode", "to", to, "subject", subject, "body", body)
		return nil
	}
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		m.from, to, subject, body,
	)
	return smtp.SendMail(m.host+":"+m.port, m.auth, m.from, []string{to}, []byte(msg))
}
