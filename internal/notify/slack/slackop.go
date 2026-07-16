// 1Password integration for the slack package (v18664-4).
//
// See telegram/telegramop.go for the design rationale (keeps the slack
// package itself dependency-free; only NewFromOp callers pay the SDK
// import cost).
//
// Per 1password-uuid-required.mdc the webhook URL must be fetched via
// the 26-character UUID exported from the onepassword package
// (onepassword.SlackWebhookUUID).
package slack

import (
	"context"
	"fmt"

	"github.com/nfsarch33/helixon-platform/internal/secrets/onepassword"
)

// NewFromOp resolves the Slack incoming webhook URL from 1Password and
// returns a fully-configured Client. The webhook URL is fetched from the
// SENTRUX_SLACK_WEBHOOK item in the HelixonSafe vault
// (onepassword.SlackWebhookUUID).
//
// Example:
//
//	cl, err := slack.NewFromOp(ctx)
//	if err != nil { return err }
//	err = cl.Send(ctx, "Hello from Helixon!")
//
// The channel override (e.g. "#fleet-critical") is supplied by the caller
// via the optional channel argument; pass "" to use the webhook's default.
func NewFromOp(ctx context.Context, channel string) (*Client, error) {
	resolver, err := onepassword.NewClient()
	if err != nil {
		return nil, fmt.Errorf("slack.NewFromOp: %w", err)
	}
	webhook, err := resolver.ResolveSlackWebhook(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack.NewFromOp: %w", err)
	}
	cl := New(Config{WebhookURL: webhook})
	if channel != "" {
		cl.channelOverride = channel
	}
	return cl, nil
}

// NewFromOpWithResolver is the test-friendly variant of NewFromOp: it
// accepts an arbitrary resolver so tests can inject a stub instead of
// hitting the real 1Password SDK. Production callers should use NewFromOp.
func NewFromOpWithResolver(ctx context.Context, resolver *onepassword.Client, channel string) (*Client, error) {
	webhook, err := resolver.ResolveSlackWebhook(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack.NewFromOpWithResolver: %w", err)
	}
	cl := New(Config{WebhookURL: webhook})
	if channel != "" {
		cl.channelOverride = channel
	}
	return cl, nil
}
