// 1Password integration for the telegram package (v18664-4).
//
// Why a separate file: importing the secrets/onepassword package from the
// notify/telegram package would create a cross-package dependency that
// pulls in the 1Password SDK into any code path that uses Telegram. By
// keeping NewFromOp in this file, the telegram package itself stays
// dependency-free; only callers that want 1Password-backed credentials
// pay the SDK import cost.
//
// Per 1password-uuid-required.mdc the item UUID must be the full
// 26-character UUID, never the display name. The known UUIDs are
// exported from the onepassword package as TelegramBotNUUID constants.
package telegram

import (
	"context"
	"fmt"

	"github.com/nfsarch33/helixon-platform/internal/secrets/onepassword"
)

// NewFromOp resolves a Telegram bot token from 1Password and returns a
// fully-configured Client. The supplied item UUID must be one of the
// Telegram bot items in the HelixonSafe vault
// (onepassword.TelegramBot1UUID / Bot2UUID / Bot3UUID).
//
// Example:
//
//	tg, err := telegram.NewFromOp(ctx, onepassword.TelegramBot1UUID, "123456789")
//	if err != nil { return err }
//	err = tg.SendMessage(ctx, "Hello from Helixon!")
//
// The chat_id is supplied by the caller because it varies per deployment
// (operator DM vs. group chat). Use ops.NewClient() for the underlying
// resolver so production wiring stays identical to the integration test.
func NewFromOp(ctx context.Context, itemUUID, chatID string) (*Client, error) {
	resolver, err := onepassword.NewClient()
	if err != nil {
		return nil, fmt.Errorf("telegram.NewFromOp: %w", err)
	}
	token, err := resolver.ResolveTelegramBotToken(ctx, itemUUID)
	if err != nil {
		return nil, fmt.Errorf("telegram.NewFromOp: %w", err)
	}
	return New(Config{
		BotToken: token,
		ChatID:   chatID,
	}), nil
}

// NewFromOpWithResolver is the test-friendly variant of NewFromOp: it
// accepts an arbitrary resolver so tests can inject a stub instead of
// hitting the real 1Password SDK. Production callers should use NewFromOp.
func NewFromOpWithResolver(ctx context.Context, resolver *onepassword.Client, itemUUID, chatID string) (*Client, error) {
	token, err := resolver.ResolveTelegramBotToken(ctx, itemUUID)
	if err != nil {
		return nil, fmt.Errorf("telegram.NewFromOpWithResolver: %w", err)
	}
	return New(Config{
		BotToken: token,
		ChatID:   chatID,
	}), nil
}
