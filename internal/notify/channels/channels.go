// Package channels provides a unified chat dispatcher for Telegram and Slack.
// v18654-4.
//
// The legacy notify.Client is email-specific (subject/body/to/from fields).
// Chat platforms use a single "text" body plus optional channel routing and
// have very different URL shapes, so they live in a separate dispatcher that
// can be selected by the operator when a session-end notification must go
// to a chat surface (operator confirmation or off-hours alerting).
//
// Design notes:
//
//   - Telegram bots and Slack webhooks are addressed by URL — there is no
//     "to" address to enumerate. The dispatcher therefore accepts a single
//     destination per call (one bot or one webhook) and lets the operator
//     route the same text to multiple surfaces by calling Send twice.
//   - Both clients already implement metric+attempt accounting via the
//     shared internal/notify/metrics package; the dispatcher simply forwards
//     status to the caller.
//   - Errors from one client are returned but do not block the second —
//     the dispatcher returns the first non-nil error so the caller can
//     log it without retrying.
package channels

import (
	"context"
	"fmt"
	"strings"

	"github.com/nfsarch33/helixon-platform/internal/notify/slack"
	"github.com/nfsarch33/helixon-platform/internal/notify/telegram"
)

// Dispatcher is the unified chat-notification entry point.
type Dispatcher struct {
	telegram *telegram.Client
	slack    *slack.Client
}

// Config configures the chat dispatcher.
type Config struct {
	TelegramBotToken string // Telegram bot token (without "bot" prefix)
	TelegramChatID   string // Telegram chat id to send to
	SlackWebhook     string // Slack incoming webhook URL
}

// New constructs a Dispatcher from a Config. Returns nil if neither Telegram
// nor Slack credentials are configured.
func New(cfg Config) *Dispatcher {
	d := &Dispatcher{}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		d.telegram = telegram.New(telegram.Config{
			BotToken: cfg.TelegramBotToken,
			ChatID:   cfg.TelegramChatID,
		})
	}
	if cfg.SlackWebhook != "" {
		d.slack = slack.NewFromURL(cfg.SlackWebhook)
	}
	return d
}

// Telegram returns the underlying Telegram client (nil if not configured).
func (d *Dispatcher) Telegram() *telegram.Client { return d.telegram }

// Slack returns the underlying Slack client (nil if not configured).
func (d *Dispatcher) Slack() *slack.Client { return d.slack }

// SendTelegram forwards text to Telegram only. Returns nil if Telegram is not
// configured.
func (d *Dispatcher) SendTelegram(ctx context.Context, text string) error {
	if d.telegram == nil {
		return fmt.Errorf("channels: telegram not configured")
	}
	return d.telegram.SendMessage(ctx, text)
}

// SendSlack forwards text to Slack only. Returns nil if Slack is not
// configured.
func (d *Dispatcher) SendSlack(ctx context.Context, text string) error {
	if d.slack == nil {
		return fmt.Errorf("channels: slack not configured")
	}
	return d.slack.Send(ctx, text)
}

// SendAll forwards the same text to every configured surface. Returns the
// first non-nil error; partial failures do not abort later sends (so a Slack
// outage does not stop a Telegram message).
func (d *Dispatcher) SendAll(ctx context.Context, text string) error {
	var first error
	if d.telegram != nil {
		if err := d.telegram.SendMessage(ctx, text); err != nil && first == nil {
			first = err
		}
	}
	if d.slack != nil {
		if err := d.slack.Send(ctx, text); err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return first
	}
	if d.telegram == nil && d.slack == nil {
		return fmt.Errorf("channels: no surfaces configured")
	}
	return nil
}

// SanitizeSummary strips control characters and truncates to a chat-friendly
// length (Telegram hard limit 4096, Slack 40000 — use 4000 as the safer
// shared ceiling).
func SanitizeSummary(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	const max = 4000
	if len(s) > max {
		s = s[:max-3] + "..."
	}
	return s
}
