// Slack Web API (chat.postMessage) transport for the slack package. v18776.
//
// Why a second transport:
//
//   - The incoming-webhook client (slack.go) is the live path today: the
//     only working credential in the vault is an incoming webhook bound to a
//     single channel, so the webhook cannot address an arbitrary channel.
//   - chat.postMessage takes a bot token (xoxb-) and an explicit channel, so
//     the moment a bot token exists the CLI can post anywhere with no code
//     change. This file is that transport, pre-wired and tested.
//
// Protocol trap this file exists to handle: Slack answers chat.postMessage
// with HTTP 200 even when the call failed, signaling the failure only in the
// JSON body as {"ok":false,"error":"invalid_auth"}. Keying success on the
// status code alone would report a send that never happened, so the body is
// always parsed and ok=false is an error.
//
// Safety posture: bounded response read, explicit per-request timeout,
// retries only on 429/5xx/network with Retry-After honored and clamped, and
// every returned error passed through Redact so a credential can never reach
// an error string, an audit event, or a log line.
//
// Usage:
//
//	cl, err := slack.NewBot(slack.BotConfig{Token: os.Getenv("SLACK_BOT_TOKEN"), Channel: "#fleet-critical"})
//	err = cl.Post(ctx, "Hello from Helixon!")

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

const (
	// botAPIBaseURL is the production Slack Web API origin.
	botAPIBaseURL = "https://slack.com"
	// botPostMessagePath is the chat.postMessage method path.
	botPostMessagePath = "/api/chat.postMessage"
	// maxAPIBodyBytes bounds the response read. Slack's chat.postMessage
	// reply is a few hundred bytes; anything past this is a hostile or
	// broken upstream and is rejected rather than buffered (unbounded
	// response buffering is a recorded P0 in this estate).
	maxAPIBodyBytes int64 = 64 << 10
	// defaultBotTimeout bounds a single HTTP attempt.
	defaultBotTimeout = 10 * time.Second
	// defaultBotAttempts is the total attempt budget (1 try + 2 retries).
	defaultBotAttempts = 3
	// maxRetryAfter clamps a server-supplied Retry-After so a hostile or
	// mistaken header cannot park the CLI for hours.
	maxRetryAfter = 30 * time.Second
	// maxBackoff caps the exponential fallback delay.
	maxBackoff = 2 * time.Second
	// baseBackoff is the first retry delay when no Retry-After is given.
	baseBackoff = 200 * time.Millisecond
	// maxAPIErrorChars caps how much of an upstream error code is echoed.
	maxAPIErrorChars = 200
	// minRedactableLen is the shortest string Redact will substitute. Short
	// strings are refused so a stray 3-character "secret" cannot mangle
	// unrelated text into uselessness.
	minRedactableLen = 8
)

// RedactedLabel is the placeholder substituted for a secret value.
const RedactedLabel = "[REDACTED]"

// Sentinel errors for the bot transport. Callers use errors.Is.
var (
	// ErrBotTokenRequired is returned when no bot token was supplied.
	ErrBotTokenRequired = errors.New("slack: bot token required (set SLACK_BOT_TOKEN)")
	// ErrBotChannelRequired is returned when no channel was supplied.
	ErrBotChannelRequired = errors.New("slack: channel required for chat.postMessage")
	// ErrAppLevelToken rejects an xapp- Socket Mode app-level token. Those
	// authenticate (auth.test returns ok) but cannot call chat.postMessage,
	// so failing at construction beats a confusing not_allowed_token_type
	// error after a live request.
	ErrAppLevelToken = errors.New("slack: xapp- app-level tokens cannot call chat.postMessage (need an xoxb- bot token)")
	// ErrResponseTooLarge is returned when the upstream body exceeds the
	// bounded read limit.
	ErrResponseTooLarge = errors.New("slack: response body exceeds bounded read limit")
)

// BotConfig configures a chat.postMessage client.
type BotConfig struct {
	// Token is the Slack bot token (xoxb-). Supplied from the environment;
	// never from argv or a config file in git.
	Token string
	// Channel is the default destination ("#fleet-critical" or a channel ID).
	Channel string
	// BaseURL overrides the Slack API origin. Empty uses botAPIBaseURL;
	// tests and staging point it at a local server.
	BaseURL string
	// Timeout bounds a single HTTP attempt. Zero uses defaultBotTimeout.
	Timeout time.Duration
	// MaxAttempts is the total attempt budget. Zero uses defaultBotAttempts.
	MaxAttempts int
	// Sleep is a test hook replacing the retry wait. Nil uses a real,
	// context-aware timer.
	Sleep func(time.Duration)
}

// BotClient posts messages through the Slack Web API.
type BotClient struct {
	token       string
	channel     string
	baseURL     string
	httpc       *http.Client
	metrics     *metrics.Registry
	maxAttempts int
	sleep       func(time.Duration)
}

// NewBot validates the config and returns a ready client. It performs no
// network I/O.
func NewBot(cfg BotConfig) (*BotClient, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, ErrBotTokenRequired
	}
	if strings.HasPrefix(token, "xapp-") {
		return nil, ErrAppLevelToken
	}
	channel := strings.TrimSpace(cfg.Channel)
	if channel == "" {
		return nil, ErrBotChannelRequired
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultBotTimeout
	}
	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = defaultBotAttempts
	}
	baseURL := strings.TrimSuffix(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = botAPIBaseURL
	}
	return &BotClient{
		token:       token,
		channel:     channel,
		baseURL:     baseURL,
		httpc:       &http.Client{Timeout: timeout},
		maxAttempts: attempts,
		sleep:       cfg.Sleep,
	}, nil
}

// WithMetrics attaches a metrics.Registry, mirroring Client.WithMetrics.
func (c *BotClient) WithMetrics(r *metrics.Registry) *BotClient {
	c.metrics = r
	return c
}

// Channel returns the default destination channel.
func (c *BotClient) Channel() string { return c.channel }

// postMessageRequest is the chat.postMessage JSON body.
type postMessageRequest struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// postMessageResponse is the subset of the chat.postMessage reply the
// transport needs. Slack signals failure here, not in the status code.
type postMessageResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Channel string `json:"channel,omitempty"`
	TS      string `json:"ts,omitempty"`
}

// Post sends text to the client's default channel.
func (c *BotClient) Post(ctx context.Context, text string) error {
	return c.PostTo(ctx, c.channel, text)
}

// PostTo sends text to an explicit channel. The retry loop covers only
// transient outcomes (429, 5xx, network); a logical failure such as
// invalid_auth or channel_not_found fails fast, because retrying it would
// burn the rate-limit budget for a result that cannot change.
func (c *BotClient) PostTo(ctx context.Context, channel, text string) error {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return ErrBotChannelRequired
	}
	body, err := json.Marshal(postMessageRequest{Channel: channel, Text: text})
	if err != nil {
		return c.redactErr(err)
	}
	if c.metrics != nil {
		c.metrics.IncAttempt(ctx, metrics.VendorSlack)
	}

	var last botOutcome
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		last = c.attempt(ctx, body)
		c.recordSendMetric(ctx, last.status)
		if last.err == nil {
			return nil
		}
		if !last.retryable || attempt == c.maxAttempts {
			break
		}
		if werr := c.wait(ctx, retryDelay(attempt, last.retryAfter)); werr != nil {
			return c.redactErr(werr)
		}
	}
	return c.redactErr(last.err)
}

// botOutcome is the classified result of one HTTP attempt.
type botOutcome struct {
	status     metrics.Status
	err        error
	retryable  bool
	retryAfter time.Duration
}

// attempt performs a single chat.postMessage request and classifies it.
func (c *BotClient) attempt(ctx context.Context, body []byte) botOutcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+botPostMessagePath, bytes.NewReader(body))
	if err != nil {
		return botOutcome{status: metrics.StatusDeadLetter, err: fmt.Errorf("slack: build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpc.Do(req)
	if err != nil {
		// A context error is terminal; anything else is a transient
		// transport fault worth one more try.
		terminal := ctx.Err() != nil
		return botOutcome{
			status:    metrics.StatusDeadLetter,
			err:       fmt.Errorf("slack: chat.postMessage transport: %w", err),
			retryable: !terminal,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	return classifyBotResponse(resp)
}

// classifyBotResponse turns an HTTP response into a typed outcome.
//
//	200 + ok=true                 -> StatusSuccess
//	429 / 5xx                     -> StatusDeadLetter, retryable
//	other non-2xx                 -> StatusBadRequest, terminal
//	200 + ok=false                -> StatusBadRequest, terminal
//	oversized / undecodable body  -> StatusDeadLetter, terminal
//
// The response body is never echoed into an error: an upstream is free to
// reflect the Authorization header back, and that must not become a log line.
func classifyBotResponse(resp *http.Response) botOutcome {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBodyBytes+1))
	if err != nil {
		return botOutcome{
			status:    metrics.StatusDeadLetter,
			err:       fmt.Errorf("slack: read response: %w", err),
			retryable: true,
		}
	}
	if int64(len(raw)) > maxAPIBodyBytes {
		return botOutcome{status: metrics.StatusDeadLetter, err: ErrResponseTooLarge}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return botOutcome{
			status:     metrics.StatusDeadLetter,
			err:        fmt.Errorf("slack: chat.postMessage HTTP %d", resp.StatusCode),
			retryable:  true,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return botOutcome{
			status: metrics.StatusBadRequest,
			err:    fmt.Errorf("slack: chat.postMessage HTTP %d", resp.StatusCode),
		}
	}
	var pr postMessageResponse
	if err := json.Unmarshal(raw, &pr); err != nil {
		return botOutcome{
			status: metrics.StatusDeadLetter,
			err:    fmt.Errorf("slack: decode chat.postMessage response: %w", err),
		}
	}
	if !pr.OK {
		return botOutcome{
			status: metrics.StatusBadRequest,
			err:    fmt.Errorf("slack: chat.postMessage failed: %s", sanitizeAPIError(pr.Error)),
		}
	}
	return botOutcome{status: metrics.StatusSuccess}
}

// recordSendMetric emits a send-status counter when a registry is attached.
func (c *BotClient) recordSendMetric(ctx context.Context, status metrics.Status) {
	if c.metrics == nil {
		return
	}
	c.metrics.IncSend(ctx, metrics.VendorSlack, status)
}

// wait pauses between retries. The production path is context-aware so a
// canceled caller is not held for the full backoff.
func (c *BotClient) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		c.sleep(d)
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// redactErr strips the bot token from an error's rendered message. The
// original error is kept as the wrapped cause so errors.Is still works,
// while Error() returns only the scrubbed text.
func (c *BotClient) redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := Redact(err.Error(), c.token)
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, cause: err}
}

// redactedError renders a scrubbed message while preserving the cause for
// errors.Is / errors.As.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// Redact replaces every occurrence of each secret with RedactedLabel.
// Secrets shorter than minRedactableLen are ignored so an accidental short
// value cannot shred unrelated text.
func Redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) < minRedactableLen {
			continue
		}
		s = strings.ReplaceAll(s, secret, RedactedLabel)
	}
	return s
}

// sanitizeAPIError makes an upstream error code safe to place in an audit
// event: control characters removed and length capped.
func sanitizeAPIError(code string) string {
	code = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, code)
	code = strings.TrimSpace(code)
	if code == "" {
		return "unknown_error"
	}
	if len(code) > maxAPIErrorChars {
		return code[:maxAPIErrorChars] + "..."
	}
	return code
}

// parseRetryAfter reads the delay-seconds form of Retry-After, clamped to
// maxRetryAfter. Unparseable or non-positive values yield 0, which makes the
// caller fall back to exponential backoff.
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// retryDelay returns the wait before the next attempt. A server-supplied
// Retry-After wins; otherwise exponential backoff capped at maxBackoff.
func retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 16 { // guard the shift
		return maxBackoff
	}
	d := baseBackoff << (attempt - 1)
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// safeURLLabel renders a URL as scheme://host only. Used in error messages
// so a webhook's secret path segment is never surfaced.
func safeURLLabel(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "<empty>"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<malformed>"
	}
	return u.Scheme + "://" + u.Host
}
