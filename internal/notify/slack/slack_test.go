// Tests for slack webhook client (no real network — only config validation
// and httptest mock). v18654-4.
package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

const validWebhook = "https://hooks.slack.com/services/T000/B000/XXXX"

func TestSend_MissingWebhook(t *testing.T) {
	c := New(Config{})
	err := c.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("Missing webhook should fail")
	}
	if !strings.Contains(err.Error(), "webhook URL required") {
		t.Fatalf("error should mention webhook URL, got %v", err)
	}
}

func TestSend_MalformedWebhook(t *testing.T) {
	c := New(Config{WebhookURL: "https://evil.example.com/webhook"})
	err := c.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("Malformed webhook should fail")
	}
	if !strings.Contains(err.Error(), "must start with") {
		t.Fatalf("error should mention prefix, got %v", err)
	}
}

func TestNewFromURL(t *testing.T) {
	c := NewFromURL(validWebhook)
	if c.Webhook() != validWebhook {
		t.Fatalf("Webhook: want %s, got %s", validWebhook, c.Webhook())
	}
}

func TestSlack_Metrics_SuccessOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer func() { srv.Close() }()
	reg := metrics.NewRegistry(nil)
	c := &Client{webhook: validWebhook, httpc: &http.Client{}, metrics: reg,
		baseURL: srv.URL, // injected for tests; see slack.go test hook
	}
	if err := c.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := reg.StatusFor(metrics.VendorSlack, metrics.StatusSuccess); got != 1 {
		t.Fatalf("StatusSuccess count: want 1, got %d", got)
	}
	if got := reg.Attempts(metrics.VendorSlack); got != 1 {
		t.Fatalf("Attempts: want 1, got %d", got)
	}
}

func TestSlack_Metrics_BadRequestOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"channel_not_found"}`))
	}))
	defer func() { srv.Close() }()
	reg := metrics.NewRegistry(nil)
	c := &Client{webhook: validWebhook, httpc: &http.Client{}, metrics: reg,
		baseURL: srv.URL}
	err := c.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error from 400")
	}
	if got := reg.StatusFor(metrics.VendorSlack, metrics.StatusBadRequest); got != 1 {
		t.Fatalf("StatusBadRequest count: want 1, got %d", got)
	}
}

func TestSlack_Metrics_DeadLetterOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer func() { srv.Close() }()
	reg := metrics.NewRegistry(nil)
	c := &Client{webhook: validWebhook, httpc: &http.Client{}, metrics: reg,
		baseURL: srv.URL}
	err := c.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error from 500")
	}
	if got := reg.StatusFor(metrics.VendorSlack, metrics.StatusDeadLetter); got != 1 {
		t.Fatalf("StatusDeadLetter count: want 1, got %d", got)
	}
}

func TestSlack_NilMetricsDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { srv.Close() }()
	c := &Client{webhook: validWebhook, httpc: &http.Client{}, baseURL: srv.URL}
	if err := c.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send with nil metrics: %v", err)
	}
}

func TestSlack_WithMetricsReturnsSame(t *testing.T) {
	reg := metrics.NewRegistry(nil)
	c := New(Config{WebhookURL: validWebhook}).WithMetrics(reg)
	if c.metrics != reg {
		t.Fatal("WithMetrics should attach the registry")
	}
}
