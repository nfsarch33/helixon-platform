package main

import (
	"math"
	"strings"
	"testing"
)

func TestEmailCostEstimate(t *testing.T) {
	tests := []struct {
		name   string
		resend bool
		brevo  bool
		want   float64
	}{
		{"resend free tier", true, false, 0.0},
		{"brevo scale up", false, true, 0.0004},
		{"both keys (resend wins)", true, true, 0.0},
		{"neither configured", false, false, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := emailCostEstimate(tt.resend, tt.brevo)
			if got != tt.want {
				t.Errorf("emailCostEstimate(%v, %v) = %v; want %v",
					tt.resend, tt.brevo, got, tt.want)
			}
		})
	}
}

func TestTelegramCostEstimate(t *testing.T) {
	tests := []struct {
		attempts int
		want     float64
	}{
		{1, 0.0001},
		{3, 0.0003},
		{0, 0.0},
	}
	const eps = 1e-9
	for _, tt := range tests {
		got := telegramCostEstimate(tt.attempts)
		if math.Abs(got-tt.want) > eps {
			t.Errorf("telegramCostEstimate(%d) = %v; want %v",
				tt.attempts, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"under limit", "hello", 10, "hello"},
		{"at limit", "hello", 5, "hello"},
		{"over limit", "hello world this is a long string", 10, "hello worl...[truncated]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q; want %q",
					tt.input, tt.maxLen, got, tt.want)
			}
			if len(got) > tt.maxLen+len("...[truncated]") {
				t.Errorf("truncate exceeded max+marker; got len=%d", len(got))
			}
		})
	}
}

func TestTruncate_LongBodyHasMarker(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := truncate(long, 100)
	if !strings.HasSuffix(got, "...[truncated]") {
		t.Errorf("truncate on long body should have ...[truncated] marker; got suffix %q",
			got[len(got)-20:])
	}
}
