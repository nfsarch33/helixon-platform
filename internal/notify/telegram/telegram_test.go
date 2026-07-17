// Tests for telegram client (no real network — only config validation).
package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

func TestSendMessage_MissingToken(t *testing.T) {
	c := New(Config{})
	err := c.SendMessage(context.Background(), "hi")
	if err == nil {
		t.Fatal("Missing token should fail")
	}
	if !strings.Contains(err.Error(), "bot token") {
		t.Fatalf("error should mention bot token, got %v", err)
	}
}

func TestSendMessageTo_MissingChatID(t *testing.T) {
	c := New(Config{BotToken: "test"})
	err := c.SendMessageTo(context.Background(), "", "hi")
	if err == nil {
		t.Fatal("Missing chat ID should fail")
	}
	if !strings.Contains(err.Error(), "chat ID") {
		t.Fatalf("error should mention chat ID, got %v", err)
	}
}

func TestNew_DefaultBaseURL(t *testing.T) {
	c := New(Config{BotToken: "test"})
	if c.baseURL != "https://api.telegram.org/bot" {
		t.Fatalf("baseURL: want default, got %s", c.baseURL)
	}
}

func TestNew_CustomBaseURL(t *testing.T) {
	c := New(Config{BotToken: "test", BaseURL: "https://custom.tg/bot"})
	if c.baseURL != "https://custom.tg/bot" {
		t.Fatalf("baseURL: want custom, got %s", c.baseURL)
	}
}

func TestTelegram_Metrics_SuccessOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer func() { srv.Close() }()
	reg := metrics.NewRegistry(nil)
	c := New(Config{BotToken: "tok", BaseURL: srv.URL + "/bot"}).WithMetrics(reg)
	if err := c.SendMessageTo(context.Background(), "123", "hi"); err != nil {
		t.Fatalf("SendMessageTo: %v", err)
	}
	if got := reg.StatusFor(metrics.VendorTelegram, metrics.StatusSuccess); got != 1 {
		t.Fatalf("StatusSuccess count: want 1, got %d", got)
	}
	if got := reg.Attempts(metrics.VendorTelegram); got != 1 {
		t.Fatalf("Attempts: want 1, got %d", got)
	}
}

func TestTelegram_Metrics_BadRequestOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"bad chat id"}`))
	}))
	defer func() { srv.Close() }()
	reg := metrics.NewRegistry(nil)
	c := New(Config{BotToken: "tok", BaseURL: srv.URL + "/bot"}).WithMetrics(reg)
	err := c.SendMessageTo(context.Background(), "123", "hi")
	if err == nil {
		t.Fatal("expected error from 400")
	}
	if got := reg.StatusFor(metrics.VendorTelegram, metrics.StatusBadRequest); got != 1 {
		t.Fatalf("StatusBadRequest count: want 1, got %d", got)
	}
}
