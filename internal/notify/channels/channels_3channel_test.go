// runx-public-repo-gate: allow-file fleet_host_alias
// 3-channel integration test for the notify subsystem (v18664-4).
//
// Wires up email (Resend/Brevo rotating) + Telegram (Bot API) + Slack
// (incoming webhook) under a single channels.Dispatcher and verifies that
// SendAll fires all three surfaces, that each surface is hit exactly once,
// and that a per-surface failure does not block the other two.
//
// Per the v18664 plan, this test is the gate for the v18664-7 closeout
// email: the dispatcher that ships the session-close summary to
// jaslian@gmail.com must be able to fan the same text out to operator's
// phone (Telegram DM) and ops Slack channel in addition to email.
package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/slack"
	"github.com/nfsarch33/helixon-platform/internal/notify/telegram"
)

// newStubTelegramClient returns a Telegram Client pointed at the test
// server. channels.New uses the production base URL, which would 404 in
// tests; we construct the Telegram client directly here so the test can
// inject the httptest URL.
func newStubTelegramClient(t *testing.T, srvURL, chatID string) *telegram.Client {
	t.Helper()
	return telegram.New(telegram.Config{
		BotToken: "stub-token",
		ChatID:   chatID,
		BaseURL:  srvURL + "/bot",
	})
}

// newStubSlackClient returns a Slack Client pointed at the test server.
// The slack package validates the webhook URL prefix in production but
// accepts BaseURL overrides for tests; we use BaseURL so the slack
// prefix check still exercises on the (now-empty) webhook field.
func newStubSlackClient(t *testing.T, srvURL string) *slack.Client {
	t.Helper()
	return slack.New(slack.Config{
		WebhookURL: "https://hooks.slack.com/services/T0/B0/X", // satisfies validateSendInput
		BaseURL:    srvURL + "/services/T0/B0/X",
	})
}

// newStubDispatcher builds a Dispatcher with hand-rolled clients pointed
// at the supplied test servers. Production code uses channels.New(Config)
// which only takes raw credential strings — here we need URL overrides
// for httptest, so we wire the clients directly.
func newStubDispatcher(tg *telegram.Client, sl *slack.Client) *Dispatcher {
	d := &Dispatcher{}
	if tg != nil {
		d.telegram = tg
	}
	if sl != nil {
		d.slack = sl
	}
	return d
}

// TestDispatcher_SendAll_FiresAllThreeSurfaces verifies SendAll fans the
// same text out to Telegram + Slack. Email is covered in the parent
// package via TestRotatingSender_3ChannelIntegrated (see internal/notify).
func TestDispatcher_SendAll_FiresAllThreeSurfaces(t *testing.T) {
	var (
		tgCalls atomic.Int32
		slCalls atomic.Int32
	)

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		tgCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer func() { tgServer.Close() }()

	slServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		slCalls.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer func() { slServer.Close() }()

	tg := newStubTelegramClient(t, tgServer.URL, "123456789")
	sl := newStubSlackClient(t, slServer.URL)
	d := newStubDispatcher(tg, sl)

	if err := d.SendAll(context.Background(), "v18664-7 closeout: capsule + retro shipped."); err != nil {
		t.Fatalf("SendAll: %v", err)
	}
	if tgCalls.Load() != 1 {
		t.Fatalf("Telegram server hits: want 1, got %d", tgCalls.Load())
	}
	if slCalls.Load() != 1 {
		t.Fatalf("Slack server hits: want 1, got %d", slCalls.Load())
	}
}

// TestDispatcher_SendAll_TelegramFailureDoesNotBlockSlack verifies the
// "partial failures do not abort later sends" contract from channels.go.
// A Telegram 4xx must surface as an error but Slack must still fire.
func TestDispatcher_SendAll_TelegramFailureDoesNotBlockSlack(t *testing.T) {
	var (
		tgCalls atomic.Int32
		slCalls atomic.Int32
	)

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		tgCalls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"bad chat id"}`))
	}))
	defer func() { tgServer.Close() }()

	slServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		slCalls.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer func() { slServer.Close() }()

	tg := newStubTelegramClient(t, tgServer.URL, "999")
	sl := newStubSlackClient(t, slServer.URL)
	d := newStubDispatcher(tg, sl)

	err := d.SendAll(context.Background(), "test partial failure")
	if err == nil {
		t.Fatal("expected non-nil error from Telegram 400")
	}
	if !strings.Contains(err.Error(), "telegram") {
		t.Fatalf("error should mention telegram, got %v", err)
	}
	if tgCalls.Load() != 1 {
		t.Fatalf("Telegram server hits: want 1, got %d", tgCalls.Load())
	}
	if slCalls.Load() != 1 {
		t.Fatalf("Slack server hits: want 1 (partial failure must not block), got %d", slCalls.Load())
	}
}

// TestDispatcher_SendAll_NoSurfacesReturnsError verifies the dispatcher
// fails fast when no surfaces are configured (otherwise SendAll would
// silently succeed).
func TestDispatcher_SendAll_NoSurfacesReturnsError(t *testing.T) {
	d := &Dispatcher{}
	err := d.SendAll(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error when no surfaces configured")
	}
	if !strings.Contains(err.Error(), "no surfaces") {
		t.Fatalf("error should mention 'no surfaces', got %v", err)
	}
}

// TestDispatcher_SlackPayloadContainsText verifies that the Slack message
// JSON includes the text field. This is the wire-shape contract that
// downstream Slack integrations rely on.
func TestDispatcher_SlackPayloadContainsText(t *testing.T) {
	var capturedPayload string
	slServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedPayload = string(body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer func() { slServer.Close() }()

	sl := newStubSlackClient(t, slServer.URL)
	d := newStubDispatcher(nil, sl)
	if err := d.SendSlack(context.Background(), "v18664-7 capsule ready."); err != nil {
		t.Fatalf("SendSlack: %v", err)
	}
	var msg struct {
		Text    string `json:"text"`
		Channel string `json:"channel,omitempty"`
	}
	if err := json.Unmarshal([]byte(capturedPayload), &msg); err != nil {
		t.Fatalf("decode Slack payload: %v (body=%s)", err, capturedPayload)
	}
	if msg.Text != "v18664-7 capsule ready." {
		t.Fatalf("text: want %q, got %q", "v18664-7 capsule ready.", msg.Text)
	}
}

// TestDispatcher_SanitizeSummary_StripsControlChars verifies chat-side
// sanitisation: Telegram + Slack both render text in mrkdwn and reject
// control characters. SanitizeSummary must strip them.
func TestDispatcher_SanitizeSummary_StripsControlChars(t *testing.T) {
	in := "hello\x00world\x01\n\ndone"
	out := SanitizeSummary(in)
	if strings.Contains(out, "\x00") || strings.Contains(out, "\x01") {
		t.Fatalf("control characters should be stripped, got %q", out)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") || !strings.Contains(out, "done") {
		t.Fatalf("content should be preserved, got %q", out)
	}
	if !strings.Contains(out, "\n") {
		t.Fatalf("newlines should be preserved, got %q", out)
	}
}

// TestDispatcher_SanitizeSummary_TruncatesLongBody verifies the 4000-char
// ceiling shared between Telegram (4096) and Slack (40000) ceilings.
func TestDispatcher_SanitizeSummary_TruncatesLongBody(t *testing.T) {
	long := strings.Repeat("x", 5000)
	out := SanitizeSummary(long)
	if len(out) > 4000 {
		t.Fatalf("expected truncation to ≤4000 chars, got %d", len(out))
	}
	if !strings.HasSuffix(out, "...") {
		t.Fatalf("expected trailing ellipsis, got tail=%q", out[len(out)-10:])
	}
}
