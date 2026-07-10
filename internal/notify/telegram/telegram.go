// Package telegram sends notifications via Telegram Bot API. v17009-6.
//
// Per CF-2026-0708-010: 3 Telegram bot tokens are stored in 1Password.
// Wire format: username (bot handle) + password (bot token).
//
// Usage:
//
//	tg, err := telegram.NewFromOp("gbqnlvhkop6lfsx4czf5gp6nga")
//	err = tg.SendMessage(ctx, "Hello from Helixon!")
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

// Client is a Telegram bot client.
type Client struct {
	botToken string
	chatID   string // default chat; can be overridden per-SendMessage
	baseURL  string
	logger   *slog.Logger
	httpc    *http.Client
	metrics  *metrics.Registry
}

// Config is the Telegram client config.
type Config struct {
	BotToken string
	ChatID   string
	BaseURL  string // default https://api.telegram.org/bot
}

// New creates a Telegram client.
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.telegram.org/bot"
	}
	return &Client{
		botToken: cfg.BotToken,
		chatID:   cfg.ChatID,
		baseURL:  cfg.BaseURL,
		logger:   slog.Default(),
		httpc:    &http.Client{Timeout: 10 * time.Second},
	}
}

// WithMetrics attaches a metrics.Registry for v17409-6 observability.
func (c *Client) WithMetrics(r *metrics.Registry) *Client {
	c.metrics = r
	return c
}

// Message is the send-message request body.
type Message struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"` // "Markdown" | "HTML"
}

// MessageResponse is the Telegram response.
type MessageResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	Result      struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

// SendMessage sends a text message to the configured chat.
func (c *Client) SendMessage(ctx context.Context, text string) error {
	return c.SendMessageTo(ctx, c.chatID, text)
}

// SendMessageTo sends a text message to a specific chat.
func (c *Client) SendMessageTo(ctx context.Context, chatID, text string) error {
	if c.botToken == "" {
		return fmt.Errorf("telegram: bot token required")
	}
	if chatID == "" {
		return fmt.Errorf("telegram: chat ID required")
	}
	if c.metrics != nil {
		c.metrics.IncAttempt(ctx, metrics.VendorTelegram)
	}
	msg := Message{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "Markdown",
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	url := c.baseURL + c.botToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		if c.metrics != nil {
			c.metrics.IncSend(ctx, metrics.VendorTelegram, metrics.StatusDeadLetter)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if c.metrics != nil {
			c.metrics.IncSend(ctx, metrics.VendorTelegram, metrics.StatusBadRequest)
		}
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var mr MessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		if c.metrics != nil {
			c.metrics.IncSend(ctx, metrics.VendorTelegram, metrics.StatusDeadLetter)
		}
		return err
	}
	if !mr.OK {
		if c.metrics != nil {
			c.metrics.IncSend(ctx, metrics.VendorTelegram, metrics.StatusBadRequest)
		}
		return fmt.Errorf("telegram: API error: %s", mr.Description)
	}
	if c.metrics != nil {
		c.metrics.IncSend(ctx, metrics.VendorTelegram, metrics.StatusSuccess)
	}
	return nil
}
