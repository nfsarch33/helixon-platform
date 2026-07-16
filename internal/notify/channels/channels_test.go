// Tests for the unified chat dispatcher. v18654-4.
package channels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/slack"
)

func TestNew_NeitherConfigured(t *testing.T) {
	d := New(Config{})
	if d.Telegram() != nil || d.Slack() != nil {
		t.Fatalf("expected nil clients, got tg=%v sl=%v", d.Telegram(), d.Slack())
	}
	if err := d.SendAll(context.Background(), "hi"); err == nil ||
		!strings.Contains(err.Error(), "no surfaces configured") {
		t.Fatalf("SendAll on empty dispatcher: want 'no surfaces configured', got %v", err)
	}
}

func TestNew_OnlySlack(t *testing.T) {
	d := New(Config{SlackWebhook: "https://hooks.slack.com/services/T/B/X"})
	if d.Slack() == nil {
		t.Fatal("Slack should be configured")
	}
	if d.Telegram() != nil {
		t.Fatal("Telegram should not be configured")
	}
	if err := d.SendTelegram(context.Background(), "hi"); err == nil ||
		!strings.Contains(err.Error(), "telegram not configured") {
		t.Fatalf("SendTelegram without Telegram: %v", err)
	}
}

func TestSendSlack_NilClient(t *testing.T) {
	d := New(Config{})
	err := d.SendSlack(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "slack not configured") {
		t.Fatalf("expected 'slack not configured', got %v", err)
	}
}

func TestSendTelegram_NilClient(t *testing.T) {
	d := New(Config{})
	err := d.SendTelegram(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "telegram not configured") {
		t.Fatalf("expected 'telegram not configured', got %v", err)
	}
}

func TestSanitizeSummary_StripsControlChars(t *testing.T) {
	in := "hello\x00\x07world\n\tline2\x7fend"
	got := SanitizeSummary(in)
	if strings.ContainsAny(got, "\x00\x07\x7f") {
		t.Fatalf("control chars not stripped: %q", got)
	}
	if !strings.Contains(got, "helloworld") || !strings.Contains(got, "line2") {
		t.Fatalf("expected sanitized text, got %q", got)
	}
	// Newlines and tabs survive.
	if !strings.Contains(got, "\n") || !strings.Contains(got, "\t") {
		t.Fatalf("expected newline+tab preserved, got %q", got)
	}
}

func TestSanitizeSummary_TruncatesLong(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := SanitizeSummary(long)
	if len(got) > 4000 {
		t.Fatalf("expected <=4000 chars, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected trailing ellipsis, got %q", got)
	}
}

func TestSanitizeSummary_PreservesLength(t *testing.T) {
	in := "short summary"
	got := SanitizeSummary(in)
	if got != in {
		t.Fatalf("expected unchanged, got %q", got)
	}
}

func TestSendSlack_LiveServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type: want application/json, got %q", ct)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	d := New(Config{}) // starts empty
	// Inject a Slack client pointed at the test server. baseURL is the test
	// hook documented in internal/notify/slack/slack.go.
	d.slack = slack.New(slack.Config{
		WebhookURL: "https://hooks.slack.com/services/T/B/X",
		BaseURL:    srv.URL,
	})
	if err := d.SendSlack(context.Background(), "hi"); err != nil {
		t.Fatalf("SendSlack: %v", err)
	}
}

func TestSendAll_BothConfigured(t *testing.T) {
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer slackSrv.Close()
	d := New(Config{}) // starts empty
	d.slack = slack.New(slack.Config{
		WebhookURL: "https://hooks.slack.com/services/T/B/X",
		BaseURL:    slackSrv.URL,
	})
	// Telegram client left nil — SendAll should still succeed via Slack
	// alone.
	if err := d.SendAll(context.Background(), "hi"); err != nil {
		t.Fatalf("SendAll with Slack-only: %v", err)
	}
}
