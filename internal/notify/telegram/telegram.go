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
//
// The orchestrator is split into five focused helpers so each has low CC
// and is independently testable (refactor v17804-3 from CC 15):
//
//	validateSendInput    - guard against missing bot token / chat ID
//	buildMessage         - construct the JSON-shaped Message payload
//	executeSendRequest   - perform the HTTP POST and surface I/O errors
//	classifyResponse     - turn HTTP status + body into a typed outcome
//	recordSendMetric     - emit one OTel-friendly observability counter
func (c *Client) SendMessageTo(ctx context.Context, chatID, text string) error {
	if err := validateSendInput(c.botToken, chatID); err != nil {
		return err
	}
	if c.metrics != nil {
		c.metrics.IncAttempt(ctx, metrics.VendorTelegram)
	}
	url := c.baseURL + c.botToken + "/sendMessage"
	body, err := json.Marshal(buildMessage(chatID, text, "Markdown"))
	if err != nil {
		return err
	}
	resp, err := executeSendRequest(ctx, c.httpc, url, body)
	if err != nil {
		if c.metrics != nil {
			c.metrics.IncSend(ctx, metrics.VendorTelegram, metrics.StatusDeadLetter)
		}
		return err
	}
	defer resp.Body.Close()
	status, err := classifyResponse(ctx, resp, c.metrics)
	recordSendMetric(ctx, c.metrics, status)
	if err != nil {
		return err
	}
	return nil
}

// validateSendInput returns an error if either the bot token or per-call
// chat ID is empty. Extracted so tests can exercise the guard without HTTP.
func validateSendInput(botToken, chatID string) error {
	if botToken == "" {
		return fmt.Errorf("telegram: bot token required")
	}
	if chatID == "" {
		return fmt.Errorf("telegram: chat ID required")
	}
	return nil
}

// buildMessage constructs the JSON-shaped Message payload. parseMode default
// is Markdown when the caller passes an empty string.
func buildMessage(chatID, text, parseMode string) Message {
	if parseMode == "" {
		parseMode = "Markdown"
	}
	return Message{
		ChatID:    chatID,
		Text:      text,
		ParseMode: parseMode,
	}
}

// executeSendRequest POSTs the JSON body to the URL using the supplied
// client and returns the response. Body is closed by the caller via defer.
func executeSendRequest(ctx context.Context, httpc *http.Client, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return httpc.Do(req)
}

// classifyResponse turns an HTTP response into a metric-Status + optional
// error. It records its own counts when a non-nil Registry is supplied so
// the SendMessageTo orchestrator stays a straight-line caller.
//
// Mapping:
//
//	200 + OK=true                              -> StatusSuccess
//	4xx (HTTP status code in [400, 500))        -> StatusBadRequest
//	200 + OK=false                             -> StatusBadRequest
//	malformed JSON / decode failure            -> StatusDeadLetter
//	network error from executeSendRequest      -> StatusDeadLetter (caller path)
func classifyResponse(ctx context.Context, resp *http.Response, reg *metrics.Registry) (metrics.Status, error) {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return metrics.StatusBadRequest,
			fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var mr MessageResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return metrics.StatusDeadLetter,
			fmt.Errorf("telegram: decode response: %w (body=%q)", err, string(body))
	}
	if !mr.OK {
		return metrics.StatusBadRequest,
			fmt.Errorf("telegram: API error: %s", mr.Description)
	}
	return metrics.StatusSuccess, nil
}

// recordSendMetric emits a single send-status counter when a metrics
// registry is attached. Centralised so the SendMessageTo orchestrator
// does not branch on the nil-registry check.
func recordSendMetric(ctx context.Context, reg *metrics.Registry, status metrics.Status) {
	if reg == nil {
		return
	}
	reg.IncSend(ctx, metrics.VendorTelegram, status)
}
