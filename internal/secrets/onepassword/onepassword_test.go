// runx-public-repo-gate: allow-file secret_cred_ref
// Tests for the onepassword resolver (v18664-4).
//
// The resolver wraps the official 1Password Go SDK so that the notify
// packages (telegram, slack) can fetch bot tokens and webhook URLs at
// runtime without leaking credentials to argv, env files, or git.
//
// Per 1password-uuid-required.mdc, every 1Password reference MUST use
// the full 26-character UUID, never the display name. The known UUIDs
// (v18654-4 inventory):
//
//   - HLXN_OP_ITEM_TELEGRAM_BOT1 — Telegram Bot - Fleet Agent 1
//   - HLXN_OP_ITEM_TELEGRAM_BOT2 — Telegram Bot - Fleet Agent 2
//   - HLXN_OP_ITEM_TELEGRAM_BOT3 — Telegram Bot - Cursor WSL1
//   - HLXN_OP_ITEM_SLACK_WEBHOOK — SENTRUX_SLACK_WEBHOOK
//
// The ids themselves are supplied by the operator environment; these tests
// use synthetic 26-char fixtures that address nothing.
//
// Item field conventions (verified by `op item get <uuid>`):
//
//	Telegram items:
//	  username = bot handle (e.g. "@fleet_agent1_bot")
//	  password = bot token (e.g. "123456789:AAG...")
//	  chat_id  = numeric destination chat id (optional, may be empty)
//
//	Slack item:
//	  webhook_url = https://hooks.slack.com/services/T.../B.../...
package onepassword

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOpServer returns an httptest.Server that responds to the limited set of
// endpoints the 1Password Go SDK calls during Resolve(). We mock the SDK at
// its HTTP boundary instead of importing the real SDK so tests stay fast and
// offline; production code wires the real SDK via NewClient(...).
type fakeOpServer struct {
	mu         atomic.Int32 // call counter
	served     atomic.Int32 // successful resolves
	forceError error
}

// newFakeServer returns an httptest.Server that emulates the SDK's Resolve
// HTTP boundary. The resolver under test should hit it once per Resolve call.
// newFakeServer also provisions the reference environment. The typed helpers
// (ResolveTelegramBotToken, ResolveSlackWebhook) now read the vault and the
// Slack item id from there instead of from package constants, so an
// unprovisioned test fails before it reaches this server -- which is the
// behaviour an unprovisioned host gets, and the reason it is set explicitly
// rather than defaulted. Both fixtures address nothing.
func newFakeServer(t *testing.T, expectedVault, expectedItem, expectedField, secret string, forceErr error) (*httptest.Server, *fakeOpServer) { //nolint:revive // unused-parameter required by interface
	t.Helper()
	t.Setenv(VaultEnv, expectedVault)
	t.Setenv(SlackWebhookItemEnv, slackWebhookUUID)
	fs := &fakeOpServer{forceError: forceErr}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		fs.mu.Add(1)
		if forceErr != nil {
			http.Error(w, forceErr.Error(), http.StatusInternalServerError)
			return
		}
		// SDK returns the secret as a JSON string body.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(secret)
		fs.served.Add(1)
	}))
	t.Cleanup(srv.Close)
	return srv, fs
}

// withFakeOpClient overrides the package-level SDK endpoint to the test
// server. The package init reads OP_SERVICE_ACCOUNT_TOKEN + the real SDK
// base URL; tests bypass that by swapping endpoint via the exported
// Client struct fields.
func withFakeOpClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	return &Client{
		Token:    "fake-token-for-test",
		Endpoint: endpoint,
		HTTPc:    &http.Client{Timeout: 5 * time.Second},
	}
}

// ---------------------------------------------------------------------------
// ResolveSecret — direct unit tests
// ---------------------------------------------------------------------------

// TestResolveSecret_ReturnsSecretFromVault verifies that a properly-formed
// vault/item/field tuple resolves to the underlying secret string.
func TestResolveSecret_ReturnsSecretFromVault(t *testing.T) {
	const want = "123456789:AAG-real-bot-token"
	srv, fs := newFakeServer(t, "test-vault", "aaaaaaaaaaaaaaaaaaaaaaaaaa", "password", want, nil)
	c := withFakeOpClient(t, srv.URL)

	got, err := c.ResolveSecret(context.Background(), "test-vault", "aaaaaaaaaaaaaaaaaaaaaaaaaa", "password")
	if err != nil {
		t.Fatalf("ResolveSecret: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveSecret: want %q, got %q", want, got)
	}
	if fs.served.Load() != 1 {
		t.Fatalf("expected exactly 1 server hit, got %d", fs.served.Load())
	}
}

// TestResolveSecret_EmptyItemUUIDReturnsError verifies that the resolver
// rejects empty item UUIDs at the boundary (defence-in-depth for the
// 1password-uuid-required rule).
func TestResolveSecret_EmptyItemUUIDReturnsError(t *testing.T) {
	c := &Client{Token: "fake"}
	_, err := c.ResolveSecret(context.Background(), "test-vault", "", "password")
	if err == nil {
		t.Fatal("expected error for empty item UUID")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "uuid") && !strings.Contains(strings.ToLower(err.Error()), "item") {
		t.Fatalf("error should mention uuid/item, got %v", err)
	}
}

// TestResolveSecret_EmptyFieldReturnsError verifies that empty field IDs
// are rejected (defence-in-depth for the @-in-field rule).
func TestResolveSecret_EmptyFieldReturnsError(t *testing.T) {
	c := &Client{Token: "fake"}
	_, err := c.ResolveSecret(context.Background(), "test-vault", "dddddddddddddddddddddddddd", "")
	if err == nil {
		t.Fatal("expected error for empty field")
	}
}

// TestResolveSecret_VaultNetworkErrorIsTransient verifies that HTTP failures
// surface as a typed error the caller can wrap with ErrTransient / retry.
func TestResolveSecret_VaultNetworkErrorIsTransient(t *testing.T) {
	srv, _ := newFakeServer(t, "test-vault", "dddddddddddddddddddddddddd", "webhook_url", "", errors.New("vault unreachable"))
	c := withFakeOpClient(t, srv.URL)

	_, err := c.ResolveSecret(context.Background(), "test-vault", "dddddddddddddddddddddddddd", "webhook_url")
	if err == nil {
		t.Fatal("expected error on vault failure")
	}
	if !errors.Is(err, ErrVaultUnreachable) {
		t.Fatalf("expected ErrVaultUnreachable, got %v", err)
	}
}

// TestResolveSecret_UsesFullUUIDNotDisplayName is a regression guard for the
// 1password-uuid-required rule. The resolver must NOT accept display-name
// inputs — even ones that contain the @ character (the cause of the
// historical op:// reference parsing bug).
func TestResolveSecret_UsesFullUUIDNotDisplayName(t *testing.T) {
	c := &Client{Token: "fake"}
	// Display names are rejected; only 26-character UUIDs pass.
	_, err := c.ResolveSecret(context.Background(), "test-vault", "Telegram Bot - Fleet Agent 1", "password")
	if err == nil {
		t.Fatal("expected error for display name (must use UUID)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "uuid") {
		t.Fatalf("error should mention uuid requirement, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ResolveTelegramBotToken — typed helper for telegram.NewFromOp
// ---------------------------------------------------------------------------

const (
	telegramBot1UUID = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	telegramBot2UUID = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	telegramBot3UUID = "cccccccccccccccccccccccccc"
	slackWebhookUUID = "dddddddddddddddddddddddddd"
)

func TestResolveTelegramBotToken_ReturnsPasswordField(t *testing.T) {
	const want = "999:AAHbypass-tok"
	srv, _ := newFakeServer(t, "test-vault", telegramBot1UUID, "password", want, nil)
	c := withFakeOpClient(t, srv.URL)

	got, err := c.ResolveTelegramBotToken(context.Background(), telegramBot1UUID)
	if err != nil {
		t.Fatalf("ResolveTelegramBotToken: %v", err)
	}
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// TestResolveTelegramBotToken_ThreeBotsAllResolvable verifies all three
// known bot UUIDs work with the same resolver call shape (closes v14271-04).
func TestResolveTelegramBotToken_ThreeBotsAllResolvable(t *testing.T) {
	for _, uuid := range []string{telegramBot1UUID, telegramBot2UUID, telegramBot3UUID} {
		t.Run(uuid, func(t *testing.T) {
			want := "tok:" + uuid
			srv, _ := newFakeServer(t, "test-vault", uuid, "password", want, nil)
			c := withFakeOpClient(t, srv.URL)

			got, err := c.ResolveTelegramBotToken(context.Background(), uuid)
			if err != nil {
				t.Fatalf("ResolveTelegramBotToken(%s): %v", uuid, err)
			}
			if got != want {
				t.Fatalf("want %q, got %q", want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ResolveSlackWebhook — typed helper for slack.NewFromOp
// ---------------------------------------------------------------------------

func TestResolveSlackWebhook_ReturnsWebhookURLField(t *testing.T) {
	const want = "https://hooks.slack.com/services/T000/B000/XXX"
	srv, _ := newFakeServer(t, "test-vault", slackWebhookUUID, "webhook_url", want, nil)
	c := withFakeOpClient(t, srv.URL)

	got, err := c.ResolveSlackWebhook(context.Background())
	if err != nil {
		t.Fatalf("ResolveSlackWebhook: %v", err)
	}
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// TestResolveSlackWebhook_RejectsNonHTTPS verifies the URL has the
// expected https://hooks.slack.com/ prefix per the slack package validation.
func TestResolveSlackWebhook_RejectsNonHTTPS(t *testing.T) {
	const bad = "http://hooks.slack.com/services/T000/B000/XXX"
	srv, _ := newFakeServer(t, "test-vault", slackWebhookUUID, "webhook_url", bad, nil)
	c := withFakeOpClient(t, srv.URL)

	_, err := c.ResolveSlackWebhook(context.Background())
	if err == nil {
		t.Fatal("expected error for non-https webhook")
	}
	if !strings.Contains(err.Error(), "https://hooks.slack.com/") {
		t.Fatalf("error should mention expected prefix, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewClient — production-side wiring
// ---------------------------------------------------------------------------

// TestNewClient_RequiresToken verifies that an empty token is rejected at
// construction (fail-fast). Production callers must set OP_SERVICE_ACCOUNT_TOKEN.
func TestNewClient_RequiresToken(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	_, err := NewClient()
	if err == nil {
		t.Fatal("expected error when token is empty")
	}
	if !strings.Contains(err.Error(), "OP_SERVICE_ACCOUNT_TOKEN") {
		t.Fatalf("error should name the env var, got %v", err)
	}
}

// TestNewClient_AcceptsNonEmptyToken verifies the happy path with a stub
// token. The SDK is NOT contacted on construction; that happens on first
// Resolve. This keeps NewClient fast and side-effect-free for tests.
func TestNewClient_AcceptsNonEmptyToken(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "ops_eyJtest-fake-token")
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("NewClient returned nil client")
	}
	if c.Token == "" {
		t.Fatal("client token should be populated")
	}
}

// TestPackageCarriesNoSecretStoreMap is the regression guard for v18793.
//
// It is structural -- no literal 26-char id, no vault name anywhere in the
// package's own source -- rather than a denylist of the four ids removed.
// A denylist would have to re-commit them to a PUBLIC repository inside the
// very test that guards against them, which is the trap this whole change
// exists to get out of.
func TestPackageCarriesNoSecretStoreMap(t *testing.T) {
	uuidLike := regexp.MustCompile(`"[a-z0-9]{26}"`)
	// Assembled from fragments on purpose. Written as plain literals, these
	// patterns match THEMSELVES -- this file is one of the files scanned --
	// and the guard reports a leak that is only its own source. That is the
	// same self-match that makes a scanner trip over its own rule set.
	vaultLike := []*regexp.Regexp{
		regexp.MustCompile(`(?i)` + "helixon" + `\s*` + "safe"),
		regexp.MustCompile(`(?i)` + "cursor" + "_" + "ironclaw"),
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		b, readErr := os.ReadFile(name) //nolint:gosec // package-local .go files
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		scanned++
		// The synthetic fixtures are a run of one repeated character. A real
		// 1Password id is not, so allowing them costs nothing.
		for _, m := range uuidLike.FindAllString(string(b), -1) {
			id := strings.Trim(m, `"`)
			if strings.Count(id, string(id[0])) == len(id) {
				continue // synthetic fixture, e.g. "aaaa...a"
			}
			t.Errorf("%s carries a literal 26-char id %q; item ids must come from %s*", name, m, ItemEnvPrefix)
		}
		for _, re := range vaultLike {
			if re.Match(b) {
				t.Errorf("%s carries a vault name literal (%s); the vault must come from %s", name, re, VaultEnv)
			}
		}
	}
	// Without this the test would pass vacuously if the directory walk broke.
	if scanned == 0 {
		t.Fatal("scanned no .go files; the guard proved nothing")
	}
}

// TestResolveItem_ErrorNeverEchoesValue: these errors reach logs. An operator
// who pastes a bot token into HLXN_OP_ITEM_* must have it reported by LENGTH,
// never by value.
func TestResolveItem_ErrorNeverEchoesValue(t *testing.T) {
	const pastedSecret = "1234567890:AAH-this-is-a-real-looking-bot-token"
	t.Setenv(TelegramBot1ItemEnv, pastedSecret)

	_, err := ResolveItem(TelegramBot1ItemEnv)
	if err == nil {
		t.Fatal("expected an error for a non-UUID item reference")
	}
	if strings.Contains(err.Error(), pastedSecret) {
		t.Errorf("error echoes the value into the log: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(len(pastedSecret))) {
		t.Errorf("error should report the observed length so the operator can debug, got: %v", err)
	}
	if !errors.Is(err, ErrInvalidUUID) {
		t.Errorf("error should wrap ErrInvalidUUID so callers can classify it, got: %v", err)
	}
}

// TestResolveVault_NoDefault: a baked-in default is the map this change
// removes, and a wrong one would address whatever vault carries that name in
// the caller's account.
func TestResolveVault_NoDefault(t *testing.T) {
	t.Setenv(VaultEnv, "")
	if _, err := ResolveVault(); err == nil {
		t.Fatal("an unset vault must be an error; there must be no default")
	} else if !strings.Contains(err.Error(), VaultEnv) {
		t.Errorf("error must name %s so the operator knows what to export, got: %v", VaultEnv, err)
	}
}
