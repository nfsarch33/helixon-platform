// Package slack sends notifications via Slack incoming webhooks. v18654-4.
//
// Slack incoming webhooks are URL-shaped (https://hooks.slack.com/services/...)
// and use HTTP POST with JSON body. The package mirrors telegram.go's shape
// (Config + New + Send + metrics integration + helpers) so the orchestrator
// pattern from telegram.go ports over without translation.
//
// Reference: 1Password item SENTRUX_SLACK_WEBHOOK (uuid ri4vhb25sijurxudb3ddjicsza)
// in vault HelixonSafe.
//
// Usage:
//
//	wh := "https://hooks.slack.com/services/T000/B000/XXX"
//	cl, err := slack.NewFromURL(wh)
//	err = cl.Send(ctx, "Hello from Helixon!")
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

// Client is a Slack webhook client.
type Client struct {
	webhook         string
	baseURL         string // empty in production; populated only by tests
	logger          *slog.Logger
	httpc           *http.Client
	metrics         *metrics.Registry
	channelOverride string // set by NewFromOpWithResolver; "" uses webhook default
}

// Config is the Slack client config.
type Config struct {
	WebhookURL string
	BaseURL    string // optional override; default https://hooks.slack.com
	Timeout    time.Duration
}

// New creates a Slack client from a Config.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		webhook: cfg.WebhookURL,
		baseURL: cfg.BaseURL,
		logger:  slog.Default(),
		httpc:   &http.Client{Timeout: timeout},
	}
}

// NewFromURL is a convenience constructor that takes the webhook URL string
// directly. Equivalent to New(Config{WebhookURL: url}).
func NewFromURL(webhookURL string) *Client {
	return New(Config{WebhookURL: webhookURL})
}

// WithMetrics attaches a metrics.Registry for v17409-6 observability.
func (c *Client) WithMetrics(r *metrics.Registry) *Client {
	c.metrics = r
	return c
}

// Webhook returns the configured webhook URL (for debug logging).
func (c *Client) Webhook() string { return c.webhook }

// resolveURL returns the destination URL for the HTTP POST. Production uses
// the configured webhook; tests override baseURL to redirect to httptest.
func (c *Client) resolveURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return c.webhook
}

// Message is the Slack webhook payload (Slack uses "text" as the body field).
type Message struct {
	Text    string `json:"text"`
	Channel string `json:"channel,omitempty"`
}

// Send posts a text message to the configured webhook.
func (c *Client) Send(ctx context.Context, text string) error {
	return c.send(ctx, Message{Text: text, Channel: c.channelOverride})
}

// Channel returns the override channel configured on this client ("" if
// the webhook's default channel is in use). Used for diagnostics + tests.
func (c *Client) Channel() string { return c.channelOverride }

// send is the orchestrator. Mirrors telegram.SendMessageTo's helper split
// (validate / build / execute / classify / record) for low-CC tests.
//
//	validateSendInput    - guard against missing webhook
//	buildMessage         - construct the JSON payload
//	executeSendRequest   - POST the body and surface I/O errors
//	classifyResponse     - turn HTTP status into typed outcome
//	recordSendMetric     - emit observability counter
func (c *Client) send(ctx context.Context, m Message) error {
	if err := validateSendInput(c.webhook); err != nil {
		return err
	}
	if c.metrics != nil {
		c.metrics.IncAttempt(ctx, metrics.VendorSlack)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	resp, err := executeSendRequest(ctx, c.httpc, c.resolveURL(), body)
	if err != nil {
		if c.metrics != nil {
			c.metrics.IncSend(ctx, metrics.VendorSlack, metrics.StatusDeadLetter)
		}
		return err
	}
	defer resp.Body.Close()
	status, err := classifyResponse(ctx, resp)
	c.recordSendMetric(ctx, status)
	if err != nil {
		return err
	}
	return nil
}

// validateSendInput returns an error if the webhook URL is empty or
// malformed (must contain hooks.slack.com/services/.../.../...).
func validateSendInput(webhook string) error {
	if webhook == "" {
		return fmt.Errorf("slack: webhook URL required")
	}
	if !strings.HasPrefix(webhook, "https://hooks.slack.com/services/") {
		return fmt.Errorf("slack: webhook must start with https://hooks.slack.com/services/ (got %q)", webhook)
	}
	return nil
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
// error. Slack returns plain text "ok" on success; any 4xx/5xx is an error.
//
//	2xx                              -> StatusSuccess
//	4xx (HTTP status code in [400, 500)) -> StatusBadRequest
//	5xx + network errors              -> StatusDeadLetter
func classifyResponse(ctx context.Context, resp *http.Response) (metrics.Status, error) { //nolint:revive // unused-parameter required by interface
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return metrics.StatusSuccess, nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return metrics.StatusBadRequest,
			fmt.Errorf("slack: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return metrics.StatusDeadLetter,
		fmt.Errorf("slack: HTTP %d: %s", resp.StatusCode, string(body))
}

// recordSendMetric emits a single send-status counter when a metrics
// registry is attached.
func (c *Client) recordSendMetric(ctx context.Context, status metrics.Status) {
	if c.metrics == nil {
		return
	}
	c.metrics.IncSend(ctx, metrics.VendorSlack, status)
}
