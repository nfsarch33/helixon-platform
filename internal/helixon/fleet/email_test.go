package fleet

import (
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     EmailConfig
		wantErr string
	}{
		{
			name:    "missing host",
			cfg:     EmailConfig{From: "a@b.com", To: []string{"c@d.com"}},
			wantErr: "host is required",
		},
		{
			name:    "missing from",
			cfg:     EmailConfig{Host: "smtp.example.com", To: []string{"c@d.com"}},
			wantErr: "from address is required",
		},
		{
			name:    "missing to",
			cfg:     EmailConfig{Host: "smtp.example.com", From: "a@b.com"},
			wantErr: "at least one recipient",
		},
		{
			name: "valid",
			cfg:  EmailConfig{Host: "smtp.example.com", From: "a@b.com", To: []string{"c@d.com"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEmailConfigAddr(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "smtp.example.com:587", EmailConfig{Host: "smtp.example.com"}.addr())
	assert.Equal(t, "smtp.example.com:465", EmailConfig{Host: "smtp.example.com", Port: 465}.addr())
}

func TestBuildMIME(t *testing.T) {
	t.Parallel()
	msg := buildMIME("from@test.com", []string{"to@test.com"}, "Test Subject", "Hello body")
	s := string(msg)
	assert.Contains(t, s, "From: from@test.com")
	assert.Contains(t, s, "To: to@test.com")
	assert.Contains(t, s, "Subject: Test Subject")
	assert.Contains(t, s, "MIME-Version: 1.0")
	assert.Contains(t, s, "Content-Type: text/plain; charset=UTF-8")
	assert.Contains(t, s, "Hello body")
}

func TestEmailReporterSendReport(t *testing.T) {
	t.Parallel()

	var captured struct {
		addr string
		from string
		to   []string
		msg  []byte
	}

	mockSend := func(addr string, a smtp.Auth, from string, to []string, msg []byte) error { //nolint:revive // unused-parameter required by interface
		captured.addr = addr
		captured.from = from
		captured.to = to
		captured.msg = msg
		return nil
	}

	cfg := EmailConfig{
		Host: "smtp.test.com",
		Port: 587,
		From: "fleet@test.com",
		To:   []string{"ops@test.com"},
	}
	reporter := &EmailReporter{cfg: cfg, send: mockSend}

	report := DailyReport{
		AgentID:     "test-agent",
		Date:        "2026-05-27",
		Total:       5,
		Completed:   4,
		Failed:      1,
		AvgLatency:  2 * time.Second,
		GeneratedAt: time.Now(),
	}

	err := reporter.SendReport(report)
	require.NoError(t, err)

	assert.Equal(t, "smtp.test.com:587", captured.addr)
	assert.Equal(t, "fleet@test.com", captured.from)
	assert.Equal(t, []string{"ops@test.com"}, captured.to)

	msgStr := string(captured.msg)
	assert.True(t, strings.Contains(msgStr, "Fleet Report: test-agent"))
	assert.True(t, strings.Contains(msgStr, "4/5 completed"))
	assert.True(t, strings.Contains(msgStr, "Fleet Daily Report"))
}

func TestEmailReporterValidationFailure(t *testing.T) {
	t.Parallel()
	reporter := NewEmailReporter(EmailConfig{})
	err := reporter.SendReport(DailyReport{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host is required")
}
