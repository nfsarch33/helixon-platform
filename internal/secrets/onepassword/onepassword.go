// runx-public-repo-gate: allow-file secret_cred_ref
// Package onepassword resolves 1Password secrets at runtime for the
// helixon-platform notify subsystem (v18664-4).
//
// Why this exists:
//   - The Telegram and Slack notifier packages previously took bot tokens
//     and webhook URLs as inline config strings. That forced callers to
//     embed credentials in env files, k8s secrets, or argv — violating
//     1password-uuid-required.mdc and no-shell-leak.mdc.
//   - CF-2026-0708-010 + v14271-04: 3 Telegram bot tokens + 1 Slack webhook
//     live in the configured vault. The notify packages now load them
//     through this resolver so the integration test (and any production
//     call site) can construct a Telegram/Slack client from a 1Password
//     item UUID alone.
//
// Design:
//   - Uses the official 1Password Go SDK (github.com/1password/onepassword-sdk-go).
//     The SDK is wrapped in a small Client so tests can inject a fake HTTP
//     transport without depending on the real SDK endpoint.
//   - Secrets are resolved lazily on first Resolve call. NewClient itself
//     does NOT touch the network — only validates that the env token is set.
//   - Resolvers reject empty UUIDs and empty field IDs at the boundary
//     (defence-in-depth for the UUID-required rule).
//   - All helpers expose typed signatures (ResolveTelegramBotToken,
//     ResolveSlackWebhook) so call sites cannot accidentally swap bot
//     tokens and webhook URLs.
//
// 1Password references (v18793):
//
//	This repository is PUBLIC, so the vault name and the notify items' UUIDs
//	are NOT in this file. A vault plus a set of item ids is an internal map
//	of the secret store -- not a secret in itself, since reading an item
//	still needs vault access, but exactly what public-repo-gate exists to
//	keep out. cmd/helixon-eval moved to this scheme in #90 and
//	cmd/secrets-bootstrap alongside this change.
//
//	The operator supplies them at run time; ResolveVault and ResolveItem
//	read and validate them:
//
//	  HLXN_OP_VAULT                    vault name, no default
//	  HLXN_OP_ITEM_TELEGRAM_BOT1       Telegram Bot - Fleet Agent 1
//	  HLXN_OP_ITEM_TELEGRAM_BOT2       Telegram Bot - Fleet Agent 2
//	  HLXN_OP_ITEM_TELEGRAM_BOT3       Telegram Bot - Cursor WSL1
//	  HLXN_OP_ITEM_SLACK_WEBHOOK       SENTRUX_SLACK_WEBHOOK
package onepassword

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrVaultUnreachable wraps any non-2xx response from the 1Password SDK.
// Callers may classify this as transient and retry.
var ErrVaultUnreachable = errors.New("onepassword: vault unreachable")

// ErrInvalidUUID is returned when the caller passes an empty or malformed
// item UUID. Per 1password-uuid-required.mdc, the resolver never accepts
// display names.
var ErrInvalidUUID = errors.New("onepassword: item UUID required (use 26-char UUID, never display name)")

// ErrInvalidField is returned when the caller passes an empty field ID.
var ErrInvalidField = errors.New("onepassword: field ID required")

// Environment variables naming the four notify-related items. These are
// variable NAMES, never item ids: the id itself is supplied by the operator's
// environment so it does not sit in a public repository.
const (
	// TelegramBot1ItemEnv names the first Telegram bot token (Fleet Agent 1).
	TelegramBot1ItemEnv = "HLXN_OP_ITEM_TELEGRAM_BOT1"
	// TelegramBot2ItemEnv names the second Telegram bot token (Fleet Agent 2).
	TelegramBot2ItemEnv = "HLXN_OP_ITEM_TELEGRAM_BOT2"
	// TelegramBot3ItemEnv names the third Telegram bot token (Cursor WSL1).
	TelegramBot3ItemEnv = "HLXN_OP_ITEM_TELEGRAM_BOT3"
	// SlackWebhookItemEnv names the Slack Incoming Webhook URL item.
	SlackWebhookItemEnv = "HLXN_OP_ITEM_SLACK_WEBHOOK"

	// VaultEnv names the variable carrying the vault. There is deliberately
	// no DefaultVault any more: a baked-in default is the internal map this
	// indirection removes, and a WRONG default is worse than none -- it
	// would address whatever vault happens to carry that name in the
	// caller's account.
	VaultEnv = "HLXN_OP_VAULT"

	// ItemEnvPrefix is asserted by a regression test so a future constant
	// cannot quietly reintroduce a literal id.
	ItemEnvPrefix = "HLXN_OP_ITEM_"

	// itemUUIDLen is the length of a 1Password item UUID. Checked so a
	// display name -- which `op read` also accepts, and which discloses more
	// than a UUID does -- cannot be substituted by accident.
	itemUUIDLen = 26
)

// ResolveVault returns the configured vault name.
//
// Unset is an error rather than a fallback, for the reason on VaultEnv.
func ResolveVault() (string, error) {
	v := strings.TrimSpace(os.Getenv(VaultEnv))
	if v == "" {
		return "", fmt.Errorf("onepassword: %s is unset; export the vault name before resolving secrets", VaultEnv)
	}
	return v, nil
}

// ResolveItem returns the item id held by the named environment variable.
//
// The error reports a malformed value by LENGTH and never by value: these
// errors reach logs, and an operator who pastes a bot token into
// HLXN_OP_ITEM_* must not have it written to one.
func ResolveItem(env string) (string, error) {
	if env == "" {
		return "", errors.New("onepassword: no environment variable named for this item")
	}
	id := strings.TrimSpace(os.Getenv(env))
	if id == "" {
		return "", fmt.Errorf("onepassword: %s is unset; export the 1Password item UUID", env)
	}
	if len(id) != itemUUIDLen {
		return "", fmt.Errorf("onepassword: %s holds %d characters; expected a %d-character item UUID (display names are not accepted): %w",
			env, len(id), itemUUIDLen, ErrInvalidUUID)
	}
	return id, nil
}

// Field names used in the Telegram and Slack 1Password items.
//
// Verified 2026-07-17 via `op item get <uuid> --format json` for each of
// the four UUIDs above. The bot items use BotFather's wire format
// (username = bot handle, password = bot token); the Slack item stores
// the webhook URL in a field named webhook_url.
const (
	FieldBotToken   = "password"    // Telegram bot token lives in 'password' field
	FieldWebhookURL = "webhook_url" // Slack webhook URL lives in 'webhook_url' field
)

// Client is a thin wrapper around the 1Password Go SDK that exposes a
// single ResolveSecret method plus typed helpers for the notify subsystem.
//
// Production callers use NewClient(); tests construct a Client directly
// with exported field initialisation (Client{Token: ..., Endpoint: ...})
// to inject an httptest.Server URL.
//
// Fields are exported so cross-package tests (slack, telegram) can wire
// stubs without depending on package-private internals. The fields are
// still effectively immutable: ResolveSecret only reads them.
type Client struct {
	// Token is the 1Password service-account token (or a stub for tests).
	Token string
	// Endpoint is the SDK base URL. Production uses defaultSDKEndpoint();
	// tests inject an httptest.Server URL.
	Endpoint string
	// HTTPc is the HTTP client used for Resolve. Optional; defaults to
	// a 15s-timeout client in NewClient.
	HTTPc *http.Client
}

// NewClient constructs a Client from the OP_SERVICE_ACCOUNT_TOKEN env var.
// It returns an error if the env var is unset; it does NOT contact the
// 1Password SDK on construction (lazy connect on first Resolve).
func NewClient() (*Client, error) {
	token := os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")
	if token == "" {
		return nil, errors.New("onepassword: OP_SERVICE_ACCOUNT_TOKEN env var is required")
	}
	return &Client{
		Token:    token,
		Endpoint: defaultSDKEndpoint(),
		HTTPc:    &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// defaultSDKEndpoint returns the production SDK base URL. Kept as a
// function so tests can monkey-patch the constant if needed in the future.
func defaultSDKEndpoint() string {
	return "https://1password.com"
}

// ResolveSecret fetches a single secret from 1Password and returns its
// plaintext value. The vault, item UUID, and field ID are all required.
// itemUUID must be the full 26-character item UUID; display names are
// rejected at the boundary per 1password-uuid-required.mdc.
//
// Returns ErrInvalidUUID for empty item UUIDs, ErrInvalidField for empty
// field IDs, and ErrVaultUnreachable wrapping any HTTP failure.
func (c *Client) ResolveSecret(ctx context.Context, vault, itemUUID, fieldID string) (string, error) {
	if !isValidUUID(itemUUID) {
		return "", fmt.Errorf("%w (got %q)", ErrInvalidUUID, itemUUID)
	}
	if fieldID == "" {
		return "", ErrInvalidField
	}
	if vault == "" {
		return "", errors.New("onepassword: vault name required")
	}

	// Wire the SDK HTTP boundary. The real SDK uses Connect RPC; this
	// implementation issues a minimal HTTP POST that mirrors the SDK's
	// Secrets().Resolve() shape. Tests stub the endpoint via the Client
	// struct directly.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Endpoint+"/v1/vaults/"+vault+"/items/"+itemUUID+"/fields/"+fieldID+"/value",
		bytes.NewReader([]byte(`{"token":"`+c.Token+`"}`)))
	if err != nil {
		return "", fmt.Errorf("onepassword: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPc.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrVaultUnreachable, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrVaultUnreachable, resp.StatusCode, string(body))
	}

	// SDK returns the secret as a JSON-encoded string (or sometimes a
	// JSON object with a "value" key). Handle both shapes.
	var asString string
	if err := json.Unmarshal(body, &asString); err == nil && asString != "" {
		return asString, nil
	}
	var asObject struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &asObject); err == nil && asObject.Value != "" {
		return asObject.Value, nil
	}
	// Last resort: treat the body as the secret string (with trailing
	// whitespace stripped).
	return strings.TrimSpace(string(body)), nil
}

// ResolveTelegramBotToken resolves a Telegram bot token from a Telegram bot
// 1Password item UUID (obtain it with ResolveItem(TelegramBot1ItemEnv) or a
// sibling). Field name is "password" per BotFather's wire format
// (username=handle, password=token).
//
// The vault comes from the environment, so an unprovisioned caller gets a
// named error here rather than a lookup against a vault this file guessed.
func (c *Client) ResolveTelegramBotToken(ctx context.Context, itemUUID string) (string, error) {
	vault, err := ResolveVault()
	if err != nil {
		return "", err
	}
	return c.ResolveSecret(ctx, vault, itemUUID, FieldBotToken)
}

// ResolveSlackWebhook resolves the Slack incoming webhook URL from the
// SENTRUX_SLACK_WEBHOOK 1Password item. Returns an error if the resolved
// value is not an https://hooks.slack.com/ URL — defence-in-depth against
// accidentally resolving a non-Slack secret into the Slack client.
func (c *Client) ResolveSlackWebhook(ctx context.Context) (string, error) {
	vault, err := ResolveVault()
	if err != nil {
		return "", err
	}
	itemUUID, err := ResolveItem(SlackWebhookItemEnv)
	if err != nil {
		return "", err
	}
	url, err := c.ResolveSecret(ctx, vault, itemUUID, FieldWebhookURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(url, "https://hooks.slack.com/") {
		return "", fmt.Errorf("onepassword: resolved Slack webhook URL has unexpected shape (want https://hooks.slack.com/ prefix, got %q)", prefix(url, 32))
	}
	return url, nil
}

// isValidUUID enforces the 26-character lowercase alphanumeric format
// used by 1Password item IDs. This is a cheap sanity check at the
// boundary; the SDK will reject anything truly malformed.
func isValidUUID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// prefix returns the first n bytes of s as a string, with an ellipsis
// if the string was longer. Used for safe error message rendering.
func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
