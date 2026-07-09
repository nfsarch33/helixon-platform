// Package notify — v17607-6 recipient unification tests.
//
// Notification target per v16101 + CARRY-044 is jaslian@gmail.com only.
// The previous default (jaslian + 2 CC recipients) violated the
// "single canonical recipient" rule that emerged after the v14502
// identity correction. v17607-6a-c: hard-code the single recipient and
// reject any non-canonical target with ErrNonCanonicalRecipient.
package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCanonicalTargets_OnlyJaslian(t *testing.T) {
	got := CanonicalTargets()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 canonical target, got %d: %v", len(got), got)
	}
	if got[0] != "jaslian@gmail.com" {
		t.Errorf("expected jaslian@gmail.com, got %s", got[0])
	}
}

func TestValidateRecipients_AcceptsCanonical(t *testing.T) {
	if err := ValidateRecipients([]string{"jaslian@gmail.com"}); err != nil {
		t.Errorf("expected nil for canonical recipient, got %v", err)
	}
}

func TestValidateRecipients_RejectsNonCanonical(t *testing.T) {
	tests := [][]string{
		{"keynear@gmail.com"},
		{"someone@example.com"},
		{"jaslian@gmail.com", "cc@example.com"},
		{""},
		{"info@oztac.com.au"},
	}
	for _, tt := range tests {
		err := ValidateRecipients(tt)
		if err == nil {
			t.Errorf("expected ErrNonCanonicalRecipient for %v, got nil", tt)
			continue
		}
		if !errors.Is(err, ErrNonCanonicalRecipient) {
			t.Errorf("for %v: expected ErrNonCanonicalRecipient, got %v", tt, err)
		}
	}
}

func TestValidateRecipients_RejectsMixed(t *testing.T) {
	// First entry canonical, second non-canonical → reject.
	err := ValidateRecipients([]string{"jaslian@gmail.com", "extra@example.com"})
	if !errors.Is(err, ErrNonCanonicalRecipient) {
		t.Errorf("expected rejection of mixed list, got %v", err)
	}
}

// TestDispatcherSend_RejectsNonCanonical verifies that Dispatcher.Send
// refuses to dispatch to any non-canonical recipient, even if the
// underlying vendor client is healthy.
func TestDispatcherSend_RejectsNonCanonical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_1"}`))
	}))
	defer srv.Close()

	d := NewDispatcher(DispatcherConfig{
		ResendClient: NewResendClient(ResendConfig{APIKey: "k", BaseURL: srv.URL}),
		BrevoClient:  NewBrevoClient(BrevoConfig{APIKey: "k", BaseURL: srv.URL}),
	})
	err := d.Send(context.Background(), Email{
		To:             []string{"stranger@example.com"},
		Subject:        "should fail",
		HTMLBody:       "<p>x</p>",
		IdempotencyKey: "test-reject-1",
	})
	if err == nil {
		t.Fatal("expected rejection, got nil")
	}
	if !errors.Is(err, ErrNonCanonicalRecipient) {
		t.Errorf("expected ErrNonCanonicalRecipient, got %v", err)
	}
}

// TestDispatcherSend_AcceptsCanonical verifies the canonical recipient
// still passes validation and reaches the vendor HTTP path.
func TestDispatcherSend_AcceptsCanonical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_1"}`))
	}))
	defer srv.Close()

	d := NewDispatcher(DispatcherConfig{
		ResendClient: NewResendClient(ResendConfig{APIKey: "k", BaseURL: srv.URL}),
		BrevoClient:  NewBrevoClient(BrevoConfig{APIKey: "k", BaseURL: srv.URL}),
	})
	err := d.Send(context.Background(), Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "ok",
		HTMLBody:       "<p>x</p>",
		IdempotencyKey: "test-accept-1",
	})
	if err != nil {
		t.Errorf("expected nil for canonical recipient, got %v", err)
	}
}
