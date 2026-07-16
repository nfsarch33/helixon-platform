// Tests for the 1Password-backed telegram constructors (v18664-4).
//
// telegram.NewFromOp / NewFromOpWithResolver are the production entry
// points that fetch the bot token from 1Password. They wrap the
// secrets/onepassword resolver; tests inject a stub via NewFromOpWithResolver
// to avoid touching the real 1Password SDK.
//
// These constructors mirror slack.NewFromOp / NewFromOpWithResolver
// and close v14271-04 (CARRY-039 — chat channels wiring).
package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/secrets/onepassword"
)

// newStubOpClient returns an onepassword.Client pointed at the supplied
// httptest server. The stub server's handler is invoked once per Resolve
// call; tests assert on call counts.
func newStubOpClient(t *testing.T, srvURL, secret string) *onepassword.Client {
	t.Helper()
	return &onepassword.Client{
		Token:    "fake-token-for-test",
		Endpoint: srvURL,
		HTTPc:    &http.Client{Timeout: 5 * time.Second},
	}
}

// TestNewFromOpWithResolver_ResolvesAndReturnsClient verifies the happy
// path: a stub server returns a bot token, the resolver fetches it,
// and the resulting Telegram client is configured to use that token.
func TestNewFromOpWithResolver_ResolvesAndReturnsClient(t *testing.T) {
	const wantToken = "999:AAH-real-bot-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`"` + wantToken + `"`))
	}))
	defer srv.Close()

	resolver := newStubOpClient(t, srv.URL, wantToken)
	cl, err := NewFromOpWithResolver(context.Background(), resolver, onepassword.TelegramBot1UUID, "123456789")
	if err != nil {
		t.Fatalf("NewFromOpWithResolver: %v", err)
	}
	if cl == nil {
		t.Fatal("NewFromOpWithResolver returned nil client")
	}
	if cl.botToken != wantToken {
		t.Fatalf("botToken: want %q, got %q", wantToken, cl.botToken)
	}
	if cl.chatID != "123456789" {
		t.Fatalf("chatID: want 123456789, got %q", cl.chatID)
	}
}

// TestNewFromOpWithResolver_AllThreeBots verifies the constructor works
// with each of the three known Telegram bot item UUIDs.
func TestNewFromOpWithResolver_AllThreeBots(t *testing.T) {
	for _, uuid := range []string{
		onepassword.TelegramBot1UUID,
		onepassword.TelegramBot2UUID,
		onepassword.TelegramBot3UUID,
	} {
		t.Run(uuid, func(t *testing.T) {
			wantToken := "tok:" + uuid
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`"` + wantToken + `"`))
			}))
			defer srv.Close()

			resolver := newStubOpClient(t, srv.URL, wantToken)
			cl, err := NewFromOpWithResolver(context.Background(), resolver, uuid, "999")
			if err != nil {
				t.Fatalf("NewFromOpWithResolver(%s): %v", uuid, err)
			}
			if cl.botToken != wantToken {
				t.Fatalf("botToken: want %q, got %q", wantToken, cl.botToken)
			}
		})
	}
}

// TestNewFromOpWithResolver_RejectsEmptyUUID verifies the constructor
// surfaces the resolver's UUID validation error rather than papering
// over it.
func TestNewFromOpWithResolver_RejectsEmptyUUID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`"anything"`))
	}))
	defer srv.Close()

	resolver := newStubOpClient(t, srv.URL, "anything")
	_, err := NewFromOpWithResolver(context.Background(), resolver, "", "999")
	if err == nil {
		t.Fatal("expected error for empty UUID")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "uuid") {
		t.Fatalf("error should mention uuid, got %v", err)
	}
}

// TestNewFromOpWithResolver_VaultErrorIsTransient verifies that a vault
// failure (HTTP 500) bubbles up unchanged so the caller can wrap it
// with ErrTransient / retry semantics.
func TestNewFromOpWithResolver_VaultErrorIsTransient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "vault unreachable", http.StatusInternalServerError)
	}))
	defer srv.Close()

	resolver := newStubOpClient(t, srv.URL, "")
	_, err := NewFromOpWithResolver(context.Background(), resolver, onepassword.TelegramBot1UUID, "999")
	if err == nil {
		t.Fatal("expected error from vault 500")
	}
	if !strings.Contains(err.Error(), "telegram.NewFromOpWithResolver") {
		t.Fatalf("error should mention telegram.NewFromOpWithResolver, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 vault call, got %d", calls.Load())
	}
}
