// Package fleet implements the Helixon agent fleet-management endpoints (email, handlers, reports).
package fleet

import (
	"fmt"
	"net/smtp"
	"strings"
)

// EmailConfig holds SMTP settings for sending fleet reports.
type EmailConfig struct {
	Host     string
	Port     int
	From     string
	To       []string
	Username string
	Password string
	TenantID string // v18686-1: multi-tenancy
}

// Validate returns an error if required fields are missing.
func (c EmailConfig) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("fleet email: host is required")
	}
	if c.From == "" {
		return fmt.Errorf("fleet email: from address is required")
	}
	if len(c.To) == 0 {
		return fmt.Errorf("fleet email: at least one recipient is required")
	}
	return nil
}

func (c EmailConfig) addr() string {
	port := c.Port
	if port == 0 {
		port = 587
	}
	return fmt.Sprintf("%s:%d", c.Host, port)
}

// EmailReporter sends fleet daily reports via SMTP.
type EmailReporter struct {
	cfg  EmailConfig
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// NewEmailReporter creates a reporter with the given SMTP config.
func NewEmailReporter(cfg EmailConfig) *EmailReporter {
	return &EmailReporter{cfg: cfg, send: smtp.SendMail}
}

// SendReport formats and sends a DailyReport via email.
func (r *EmailReporter) SendReport(report DailyReport) error {
	if err := r.cfg.Validate(); err != nil {
		return err
	}

	subject := fmt.Sprintf("Fleet Report: %s — %s (%d/%d completed)",
		report.AgentID, report.Date, report.Completed, report.Total)

	body := FormatReport(report)

	msg := buildMIME(r.cfg.From, r.cfg.To, subject, body)

	var auth smtp.Auth
	if r.cfg.Username != "" {
		auth = smtp.PlainAuth("", r.cfg.Username, r.cfg.Password, r.cfg.Host)
	}

	return r.send(r.cfg.addr(), auth, r.cfg.From, r.cfg.To, msg)
}

func buildMIME(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString(body)
	return []byte(b.String())
}
