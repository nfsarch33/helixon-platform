// Tests for the 1Password-backed slack constructors (v18664-4).
//
// slack.NewFromOp / NewFromOpWithResolver are the production entry
// points that fetch the webhook URL from 1Password. They wrap the
// secrets/onepassword resolver; tests inject a stub via NewFromOpWithResolver
// to avoid touching the real 1Password SDK.
//
// These constructors mirror telegram.NewFromOp / NewFromOpWithResolver
// and close v14271-04 (CARRY-039 — chat channels wiring).
package slack

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
// path: a stub server returns a webhook URL, the resolver fetches it,
// and the resulting Slack client is configured to call that URL.
func TestNewFromOpWithResolver_ResolvesAndReturnsClient(t *testing.T) {
	const wantURL = "https://hooks.slack.com/services/T0/B0/XXX"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`"` + wantURL + `"`))
	}))
	defer func() { srv.Close() }()

	resolver := newStubOpClient(t, srv.URL, wantURL)
	cl, err := NewFromOpWithResolver(context.Background(), resolver, "")
	if err != nil {
		t.Fatalf("NewFromOpWithResolver: %v", err)
	}
	if cl == nil {
		t.Fatal("NewFromOpWithResolver returned nil client")
	}
	if cl.Channel() != "" {
		t.Fatalf("Channel: want empty (default), got %q", cl.Channel())
	}
	if cl.webhook != wantURL {
		t.Fatalf("webhook: want %q, got %q", wantURL, cl.webhook)
	}
}

// TestNewFromOpWithResolver_ChannelOverride verifies the channel override
// is propagated through to the Client (and would be included in the
// outbound Slack message JSON).
func TestNewFromOpWithResolver_ChannelOverride(t *testing.T) {
	const wantURL = "https://hooks.slack.com/services/T0/B0/XXX"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`"` + wantURL + `"`))
	}))
	defer func() { srv.Close() }()

	resolver := newStubOpClient(t, srv.URL, wantURL)
	cl, err := NewFromOpWithResolver(context.Background(), resolver, "#fleet-critical")
	if err != nil {
		t.Fatalf("NewFromOpWithResolver: %v", err)
	}
	if cl.Channel() != "#fleet-critical" {
		t.Fatalf("Channel: want #fleet-critical, got %q", cl.Channel())
	}
}

// TestNewFromOpWithResolver_RejectsBadURL verifies the resolver's URL
// validation rejects non-Slack URLs (defence-in-depth: the slack client
// would also reject them, but failing in NewFromOp produces a cleaner
// error message).
func TestNewFromOpWithResolver_RejectsBadURL(t *testing.T) {
	const bad = "http://malicious.example.com/services/T0/B0/XXX"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`"` + bad + `"`))
	}))
	defer func() { srv.Close() }()

	resolver := newStubOpClient(t, srv.URL, bad)
	_, err := NewFromOpWithResolver(context.Background(), resolver, "")
	if err == nil {
		t.Fatal("expected error for non-Slack URL")
	}
	if !strings.Contains(err.Error(), "https://hooks.slack.com/") {
		t.Fatalf("error should mention expected prefix, got %v", err)
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
	defer func() { srv.Close() }()

	resolver := newStubOpClient(t, srv.URL, "")
	_, err := NewFromOpWithResolver(context.Background(), resolver, "")
	if err == nil {
		t.Fatal("expected error from vault 500")
	}
	if !strings.Contains(err.Error(), "slack.NewFromOp") {
		t.Fatalf("error should mention slack.NewFromOp, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 vault call, got %d", calls.Load())
	}
}
