// Tests for telegram client (no real network — only config validation).
package telegram

import (
	"context"
	"strings"
	"testing"
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
