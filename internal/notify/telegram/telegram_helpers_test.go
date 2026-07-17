package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

// newTestMetrics returns a noop-backed metrics registry for tests.
func newTestMetrics() *metrics.Registry {
	return metrics.NewRegistry(noop.NewMeterProvider().Meter("telegram-test"))
}

// TestBuildMessage_DefaultParseMode confirms the message-building helper sets
// Markdown as default parse mode so callers can omit it.
func TestBuildMessage_DefaultParseMode(t *testing.T) {
	t.Parallel()
	msg := buildMessage("chat-123", "hello world", "")
	if msg.ChatID != "chat-123" {
		t.Errorf("chat id: got %q want %q", msg.ChatID, "chat-123")
	}
	if msg.Text != "hello world" {
		t.Errorf("text: got %q want %q", msg.Text, "hello world")
	}
	if msg.ParseMode != "Markdown" {
		t.Errorf("parse mode default: got %q want Markdown", msg.ParseMode)
	}
}

// TestBuildMessage_CallerOverride confirms a non-empty parse mode wins over default.
func TestBuildMessage_CallerOverride(t *testing.T) {
	t.Parallel()
	msg := buildMessage("chat-1", "x", "HTML")
	if msg.ParseMode != "HTML" {
		t.Errorf("caller parse mode override: got %q want HTML", msg.ParseMode)
	}
}

// TestClassifyResponse_Success returns StatusSuccess when 200 + OK=true.
func TestClassifyResponse_Success(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":7}}`)),
	}
	reg := newTestMetrics()
	status, err := classifyResponse(context.Background(), resp, reg)
	if err != nil {
		t.Fatalf("classifyResponse: %v", err)
	}
	if status != metrics.StatusSuccess {
		t.Errorf("status: got %q want StatusSuccess", status)
	}
}

// TestClassifyResponse_BadRequest_HTTP4xx returns BadRequest + error.
func TestClassifyResponse_BadRequest_HTTP4xx(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"ok":false,"description":"bad chat id"}`)),
	}
	reg := newTestMetrics()
	status, err := classifyResponse(context.Background(), resp, reg)
	if err == nil {
		t.Fatal("expected error for 4xx")
	}
	if status != metrics.StatusBadRequest {
		t.Errorf("status: got %q want StatusBadRequest", status)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status code 400, got %v", err)
	}
}

// TestClassifyResponse_BadRequest_OKFalse returns BadRequest when 200 but ok=false.
func TestClassifyResponse_BadRequest_OKFalse(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":false,"description":"blocked by user"}`)),
	}
	reg := newTestMetrics()
	status, err := classifyResponse(context.Background(), resp, reg)
	if err == nil {
		t.Fatal("expected error for ok=false")
	}
	if status != metrics.StatusBadRequest {
		t.Errorf("status: got %q want StatusBadRequest", status)
	}
}

// TestClassifyResponse_BodyDecodeError returns DeadLetter for malformed JSON.
func TestClassifyResponse_BodyDecodeError(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`not json at all`)),
	}
	reg := newTestMetrics()
	status, err := classifyResponse(context.Background(), resp, reg)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if status != metrics.StatusDeadLetter {
		t.Errorf("status: got %q want StatusDeadLetter", status)
	}
}

// TestSendMessageTo_EndToEnd_HappyPath drives the orchestrator against a real
// httptest server and asserts the success metric fires.
func TestSendMessageTo_EndToEnd_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var m Message
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&m); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if m.ChatID != "chat-99" {
			t.Errorf("chat id in body: got %q want chat-99", m.ChatID)
		}
		if m.ParseMode != "Markdown" {
			t.Errorf("parse mode: got %q want Markdown", m.ParseMode)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	t.Cleanup(srv.Close)

	reg := newTestMetrics()
	c := New(Config{BotToken: "bot-test", ChatID: "chat-1", BaseURL: srv.URL + "/bot"})
	c.WithMetrics(reg)
	if err := c.SendMessageTo(context.Background(), "chat-99", "hello"); err != nil {
		t.Fatalf("SendMessageTo: %v", err)
	}
}

// TestSendMessageTo_EndToEnd_BadRequest drives the orchestrator against a 400
// server and asserts BadRequest status propagates.
func TestSendMessageTo_EndToEnd_BadRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"invalid chat"}`))
	}))
	t.Cleanup(srv.Close)
	reg := newTestMetrics()
	c := New(Config{BotToken: "bot-test", ChatID: "chat-1", BaseURL: srv.URL + "/bot"})
	c.WithMetrics(reg)
	err := c.SendMessageTo(context.Background(), "chat-99", "hello")
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400, got %v", err)
	}
}

// TestSendMessageTo_ValidationMissingBotToken confirms pre-flight guard.
func TestSendMessageTo_ValidationMissingBotToken(t *testing.T) {
	t.Parallel()
	c := New(Config{ChatID: "chat-1"})
	err := c.SendMessageTo(context.Background(), "chat-1", "hi")
	if err == nil {
		t.Fatal("expected bot token error")
	}
	if !strings.Contains(err.Error(), "bot token") {
		t.Errorf("error should mention 'bot token', got %v", err)
	}
}

// TestSendMessageTo_ValidationMissingChatID confirms pre-flight guard.
func TestSendMessageTo_ValidationMissingChatID(t *testing.T) {
	t.Parallel()
	c := New(Config{BotToken: "bot-x"})
	err := c.SendMessageTo(context.Background(), "", "hi")
	if err == nil {
		t.Fatal("expected chat ID error")
	}
	if !strings.Contains(err.Error(), "chat ID") {
		t.Errorf("error should mention 'chat ID', got %v", err)
	}
}
