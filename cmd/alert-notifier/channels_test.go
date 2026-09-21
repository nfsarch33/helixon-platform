// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/slack"
	"github.com/nfsarch33/helixon-platform/internal/notify/telegram"
)

// v18856: urgent HITL notifications must reach Slack (#fleet-critical and
// #cursor-updates) and Telegram, not email alone. These tests pin the cmd
// wiring: env → clients, per-channel independence, any-success semantics,
// and the truncation that keeps digest bodies inside vendor limits.

// postStub records JSON bodies posted at it, behind any URL shape the
// vendor clients produce.
type postStub struct {
	mu     sync.Mutex
	bodies []string
	srv    *httptest.Server
	fail   bool
}

// stubWebhook is a placeholder that passes the client's prefix validation;
// BaseURL redirects the actual POST to the local stub.
const stubWebhook = "https://hooks.slack.com/services/T000TEST/B000TEST/XXXXXXXXXXXXXXXX"

func newPostStub(t *testing.T, fail bool, successBody string) *postStub {
	t.Helper()
	s := &postStub{fail: fail}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()
		if s.fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(successBody))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *postStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *postStub) lastBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		return ""
	}
	return s.bodies[len(s.bodies)-1]
}

func testEmail() notify.Email {
	return notify.Email{
		To:       []string{"ops@example.test"},
		Subject:  "[HELIXON] 1 new / 7 still firing",
		TextBody: "FleetDoctorRed severity=warning\nTailscaleNodeDown severity=critical\n",
	}
}

func TestBuildChannelsFromEnv(t *testing.T) {
	t.Run("all set", func(t *testing.T) {
		getenv := func(k string) string {
			switch k {
			case slackFleetCriticalEnv:
				return "https://hooks.slack.test/fleet-critical"
			case slackCursorUpdatesEnv:
				return "https://hooks.slack.test/cursor-updates"
			case telegramTokenEnv:
				return "000000:test-token"
			case telegramChatEnv:
				return "-1001234567890"
			}
			return ""
		}
		stderr := &bytes.Buffer{}
		ch := buildChannels(getenv, stderr)
		if ch.fleetCritical == nil || ch.cursorUpdates == nil || ch.telegram == nil {
			t.Fatalf("expected all three channels configured, got %+v", ch)
		}
		if strings.Contains(stderr.String(), telegramChatEnv) {
			t.Errorf("unexpected skip note with chat id set: %s", stderr)
		}
	})
	t.Run("telegram token without chat id skips loudly", func(t *testing.T) {
		getenv := func(k string) string {
			if k == telegramTokenEnv {
				return "000000:test-token"
			}
			return ""
		}
		stderr := &bytes.Buffer{}
		ch := buildChannels(getenv, stderr)
		if ch.telegram != nil {
			t.Fatal("telegram client built without chat id")
		}
		if !strings.Contains(stderr.String(), telegramChatEnv) {
			t.Errorf("stderr missing skip note naming %s: %s", telegramChatEnv, stderr)
		}
	})
	t.Run("nothing set is empty not error", func(t *testing.T) {
		ch := buildChannels(func(string) string { return "" }, &bytes.Buffer{})
		if !ch.empty() {
			t.Fatalf("expected empty channels, got %+v", ch)
		}
	})
}

func TestOpsSenderAnySuccess(t *testing.T) {
	ok := newPostStub(t, false, "ok")
	fail := newPostStub(t, true, "")
	ch := channels{
		cursorUpdates: slack.New(slack.Config{WebhookURL: stubWebhook, BaseURL: ok.srv.URL}),
		fleetCritical: slack.New(slack.Config{WebhookURL: stubWebhook, BaseURL: fail.srv.URL}),
	}
	s := ch.opsSender(&bytes.Buffer{})
	if err := s.Send(context.Background(), testEmail()); err != nil {
		t.Fatalf("ops Send err = %v, want nil when one channel delivered", err)
	}
	if ok.count() != 1 {
		t.Fatalf("ok webhook posts = %d, want 1", ok.count())
	}
}

func TestOpsSenderAllFail(t *testing.T) {
	fail := newPostStub(t, true, "")
	ch := channels{
		cursorUpdates: slack.New(slack.Config{WebhookURL: stubWebhook, BaseURL: fail.srv.URL}),
	}
	s := ch.opsSender(&bytes.Buffer{})
	if err := s.Send(context.Background(), testEmail()); err == nil {
		t.Fatal("ops Send err = nil, want error when every channel failed")
	}
}

func TestUrgentSenderFansOutToSlackAndTelegram(t *testing.T) {
	fleet := newPostStub(t, false, "ok")
	cursor := newPostStub(t, false, "ok")
	tg := newPostStub(t, false, `{"ok":true}`)
	ch := channels{
		fleetCritical: slack.New(slack.Config{WebhookURL: stubWebhook, BaseURL: fleet.srv.URL}),
		cursorUpdates: slack.New(slack.Config{WebhookURL: stubWebhook, BaseURL: cursor.srv.URL}),
		telegram: telegram.New(telegram.Config{
			BotToken: "000000:test-token",
			ChatID:   "-1001234567890",
			BaseURL:  tg.srv.URL + "/bot",
		}),
	}
	s := ch.urgentSender(&bytes.Buffer{})
	email := testEmail()
	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("urgent Send err = %v", err)
	}
	if fleet.count() != 1 || cursor.count() != 1 {
		t.Fatalf("posts fleet=%d cursor=%d, want 1/1", fleet.count(), cursor.count())
	}
	if tg.count() != 1 {
		t.Fatalf("telegram sends = %d, want 1", tg.count())
	}
	if !strings.Contains(tg.lastBody(), "chat_id") {
		t.Errorf("telegram body missing chat_id: %s", tg.lastBody())
	}
}

func TestTruncateBodyVendorLimits(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := truncateBody(long, 2900)
	if len(got) > 2900 {
		t.Fatalf("truncated len = %d, want <= 2900", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated body missing ellipsis suffix: %q", got[len(got)-3:])
	}
	if got := truncateBody("short", 2900); got != "short" {
		t.Errorf("short body mutated: %q", got)
	}
}
