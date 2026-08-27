// Command notify exposes the helixon-platform/internal/notify package as a
// CLI for ad-hoc operator use (cost observability + Telegram live-send with
// 3-strike fallback). v17508-4 / v17508-5.
//
// Usage:
//
//	notify --cost --to "user@host" \
//	       --subject "[END]" --body-file body.md \
//	       --idempotency-key v17508-end \
//	       [--via email|telegram|slack|both|all] \
//	       [--slack-channel "#fleet-critical"] \
//	       [--dry-run]
//
// --via accepts a comma-separated list ("email,slack"). "both" keeps its
// historical meaning of email+telegram; "all" adds slack.
//
// Slack credentials are read from the environment only - never argv, so a
// token cannot land in a shell history or a process listing:
//
//	SLACK_BOT_TOKEN         xoxb- bot token; selects chat.postMessage mode
//	SLACK_CHANNEL           default channel when --slack-channel is absent
//	SENTRUX_SLACK_WEBHOOK   incoming-webhook URL; selects webhook mode
//	SLACK_WEBHOOK_URL       alias for SENTRUX_SLACK_WEBHOOK
//	OP_SERVICE_ACCOUNT_TOKEN  last resort: resolve the webhook from 1Password
//	HLXN_SLACK_API_BASE     Slack API origin override (tests / staging)
//
// In --dry-run mode (default when keys are empty) the command renders the
// dispatch, computes costs, applies the 3-strike policy, and emits a
// structured audit event to stdout as NDJSON. The real network call is
// skipped. This mirrors send-end-email's audit-first posture.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/channels"
	"github.com/nfsarch33/helixon-platform/internal/notify/endemail"
	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
	"github.com/nfsarch33/helixon-platform/internal/notify/slack"
	"github.com/nfsarch33/helixon-platform/internal/notify/telegram"
)

func main() {
	os.Exit(runNotifyCmd(os.Args[1:]))
}

// notifyFlags holds the parsed CLI flags and env-var fallbacks for the
// notify command. v17714-1: extracted from main() so the dispatcher
// stays under CC ≤6.
type notifyFlags struct {
	plan      string
	subject   string
	bodyFile  string
	idemKey   string
	jobID     string
	resendKey string
	brevoKey  string
	tgToken   string
	tgChatID  string
	via       string
	cost      bool
	dryRun    bool

	// Slack inputs. Credentials are env-only; only the channel (not a
	// secret) is settable on argv.
	slackChannel string
	slackToken   string // SLACK_BOT_TOKEN
	slackWebhook string // SENTRUX_SLACK_WEBHOOK / SLACK_WEBHOOK_URL
	slackAPIBase string // HLXN_SLACK_API_BASE
	opToken      string // OP_SERVICE_ACCOUNT_TOKEN
}

// notifyOptions groups flag/env inputs for runNotifyCmd.
type notifyOptions struct {
	flags     notifyFlags
	bodyMD    string
	timestamp string
}

// runNotifyCmd is the testable entry point of the notify CLI. It returns
// the process exit code rather than calling os.Exit directly. v17714-1:
// extracted from main() to enable TDD + CC reduction.
func runNotifyCmd(args []string) int {
	opts, rc := parseNotifyArgs(args)
	if rc != 0 {
		return rc
	}

	audit := buildBaseAudit(opts)

	if viaIncludes(opts.flags.via, "email") {
		populateEmailAudit(audit, opts)
	}
	if viaIncludes(opts.flags.via, "telegram") {
		populateTelegramAudit(audit, opts)
	}
	if viaIncludes(opts.flags.via, viaSlack) {
		populateSlackAudit(audit, &opts)
	}

	return emitAudit(audit, opts.flags)
}

// parseNotifyArgs parses flags + env fallbacks + body file. Returns
// (opts, exitCode). v17714-1: extracted from runNotifyCmd.
func parseNotifyArgs(args []string) (notifyOptions, int) {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var f notifyFlags
	fs.StringVar(&f.plan, "plan", "", "plan range (e.g. v17501-v17600)")
	fs.StringVar(&f.subject, "subject", "", "notification subject")
	fs.StringVar(&f.bodyFile, "body-file", "", "path to markdown body")
	fs.StringVar(&f.idemKey, "idempotency-key", "", "idempotency key (required)")
	fs.StringVar(&f.jobID, "job-id", "", "cost-attribution job id")
	fs.StringVar(&f.resendKey, "resend-key", "", "Resend API key (or RESEND_API_KEY env)")
	fs.StringVar(&f.brevoKey, "brevo-key", "", "Brevo API key (or BREVO_API_KEY env)")
	fs.StringVar(&f.tgToken, "telegram-token", "", "Telegram bot token (or TELEGRAM_BOT_TOKEN env)")
	fs.StringVar(&f.tgChatID, "telegram-chat-id", "", "Telegram chat ID (or TELEGRAM_CHAT_ID env)")
	fs.StringVar(&f.via, "via", "email", "send path: email | telegram | slack | both | all (comma-separated allowed)")
	fs.StringVar(&f.slackChannel, "slack-channel", "", "Slack channel override (or SLACK_CHANNEL env)")
	fs.BoolVar(&f.cost, "cost", false, "emit cost observability in audit event")
	fs.BoolVar(&f.dryRun, "dry-run", false, "skip network send, emit audit event")
	var fromAddr string
	fs.StringVar(&fromAddr, "from", "noreply@oztac.com.au", "From address (email)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return notifyOptions{}, 2
	}
	if f.idemKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --idempotency-key required")
		return notifyOptions{}, 2
	}
	if err := validateVia(f.via); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return notifyOptions{}, 2
	}

	f.resendKey = envOrFlag(f.resendKey, "RESEND_API_KEY")
	f.brevoKey = envOrFlag(f.brevoKey, "BREVO_API_KEY")
	f.tgToken = envOrFlag(f.tgToken, "TELEGRAM_BOT_TOKEN")
	f.tgChatID = envOrFlag(f.tgChatID, "TELEGRAM_CHAT_ID")
	f.slackChannel = envOrFlag(f.slackChannel, "SLACK_CHANNEL")
	f.slackToken = os.Getenv("SLACK_BOT_TOKEN")
	f.slackWebhook = envOrFlag(os.Getenv("SENTRUX_SLACK_WEBHOOK"), "SLACK_WEBHOOK_URL")
	f.slackAPIBase = os.Getenv("HLXN_SLACK_API_BASE")
	f.opToken = os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")

	bodyMD, rc := readBodyFile(f.bodyFile)
	if rc != 0 {
		return notifyOptions{}, rc
	}

	return notifyOptions{
		flags:     f,
		bodyMD:    bodyMD,
		timestamp: time.Now().UTC().Format(time.RFC3339),
	}, 0
}

// envOrFlag returns env value if flag value is empty. v17714-1: extracted.
func envOrFlag(flagVal, envName string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envName)
}

// readBodyFile reads the optional body file. v17714-1: extracted.
func readBodyFile(path string) (string, int) {
	if path == "" {
		return "", 0
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: read body-file: %v\n", err)
		return "", 2
	}
	return string(raw), 0
}

// buildBaseAudit builds the audit event's base fields. v17714-1: extracted.
func buildBaseAudit(opts notifyOptions) map[string]any {
	f := opts.flags
	return map[string]any{
		"ts":                 opts.timestamp,
		"event":              "notify_attempt",
		"plan":               f.plan,
		"job_id":             f.jobID,
		"idempotency_key":    f.idemKey,
		"via":                f.via,
		"cost_requested":     f.cost,
		"dry_run":            f.dryRun,
		"resend_key_set":     f.resendKey != "",
		"brevo_key_set":      f.brevoKey != "",
		"telegram_token_set": f.tgToken != "",
		"telegram_chat_set":  f.tgChatID != "",
	}
}

// Recognized --via tokens.
const (
	viaEmail    = "email"
	viaTelegram = "telegram"
	viaSlack    = "slack"
	// viaBoth keeps its historical meaning: email + telegram. Slack was
	// added after this token was already in operator muscle memory and in
	// scripts, so "both" deliberately does NOT grow a third surface.
	viaBoth = "both"
	// viaAll is the opt-in token for every surface including slack.
	viaAll = "all"
)

// viaExpansion maps a --via token to the concrete surfaces it engages.
var viaExpansion = map[string][]string{
	viaEmail:    {viaEmail},
	viaTelegram: {viaTelegram},
	viaSlack:    {viaSlack},
	viaBoth:     {viaEmail, viaTelegram},
	viaAll:      {viaEmail, viaTelegram, viaSlack},
}

// viaTokens splits a --via spec into normalised, non-empty tokens.
func viaTokens(via string) []string {
	parts := strings.Split(via, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// viaIncludes reports whether the via spec engages the named surface.
// v18776: accepts a comma-separated list so slack can be combined with the
// pre-existing paths without redefining "both".
func viaIncludes(via, want string) bool {
	for _, token := range viaTokens(via) {
		for _, surface := range viaExpansion[token] {
			if surface == want {
				return true
			}
		}
	}
	return false
}

// validateVia rejects an unknown or empty --via spec at parse time, so a
// typo fails loudly instead of silently sending nothing.
func validateVia(via string) error {
	tokens := viaTokens(via)
	if len(tokens) == 0 {
		return errors.New("--via must not be empty (want email|telegram|slack|both|all)")
	}
	for _, token := range tokens {
		if _, ok := viaExpansion[token]; !ok {
			return fmt.Errorf("unknown --via value %q (want email|telegram|slack|both|all, comma-separated)", token)
		}
	}
	return nil
}

// populateEmailAudit adds email-template fields to the audit map.
// v17714-1: extracted from runNotifyCmd.
func populateEmailAudit(audit map[string]any, opts notifyOptions) {
	f := opts.flags
	tmpl := endemail.Template{
		Plan:         f.plan,
		Subject:      f.subject,
		BodyMarkdown: opts.bodyMD,
		JobID:        f.jobID,
		IdempKey:     f.idemKey,
		TenantID:     "cursor-global-kb",
	}
	m := tmpl.Build()
	audit["email_subject"] = m.Subject
	audit["email_to"] = m.To
	audit["email_cc"] = m.CC
	audit["email_html_len"] = len(m.HTMLBody)
	audit["email_text_len"] = len(m.TextBody)
	if f.cost {
		audit["email_cost_estimate_usd"] = emailCostEstimate(f.resendKey != "", f.brevoKey != "")
	}
}

// populateTelegramAudit adds telegram-fields to the audit map, including
// the optional 3-strike send. v17714-1: extracted from runNotifyCmd.
func populateTelegramAudit(audit map[string]any, opts notifyOptions) {
	f := opts.flags
	if f.tgToken == "" || f.tgChatID == "" {
		audit["telegram_blocker"] = "telegram bot token or chat ID not configured in 1Password (CF-2026-0708-010 partial)"
		return
	}
	if f.dryRun {
		audit["telegram_result"] = "dry-run"
		return
	}
	tg := telegram.New(telegram.Config{BotToken: f.tgToken, ChatID: f.tgChatID})
	tgAtt, tgRes, tgErr := telegramWithStrikes(tg, f.subject, opts.bodyMD, 3)
	audit["telegram_attempts"] = tgAtt
	audit["telegram_result"] = tgRes
	if tgErr != nil {
		audit["telegram_error"] = tgErr.Error()
	}
	if f.cost {
		audit["telegram_cost_estimate_usd"] = telegramCostEstimate(tgAtt)
	}
}

// emitAudit prints the audit map and the DRY-RUN marker. Returns the
// process exit code. v17714-1: extracted from runNotifyCmd.
//
// v18776: a Slack send that failed exits non-zero. The audit event is still
// printed - the operator needs the reason - but the exit code must not
// report success for a message that was never delivered.
func emitAudit(audit map[string]any, f notifyFlags) int {
	dryRun := isDryRun(audit, f)
	switch {
	case dryRun:
		audit["result"] = "dry-run"
	case audit["slack_result"] == "sent":
		audit["result"] = "sent"
	default:
		audit["result"] = "rendered-no-send"
	}
	out, _ := json.MarshalIndent(audit, "", "  ")
	fmt.Println(string(out))
	if dryRun {
		fmt.Fprintln(os.Stderr, "DRY-RUN: skipping live network send")
		return 0
	}
	if audit["slack_result"] == "failed" {
		fmt.Fprintln(os.Stderr, "ERROR: slack send failed; see slack_error in the audit event")
		return 1
	}
	return 0
}

// isDryRun reports whether the dispatch should skip the live send.
// v17714-1: extracted from emitAudit.
func isDryRun(audit map[string]any, f notifyFlags) bool { //nolint:revive // unused-parameter required by interface
	if f.dryRun {
		return true
	}
	// A configured Slack surface is a real send path in its own right: it
	// must not be downgraded to dry-run just because no email or Telegram
	// credential happens to be present.
	if viaIncludes(f.via, viaSlack) && slackMode(&f) != slackModeUnconfigured {
		return false
	}
	noEmailKeys := f.resendKey == "" && f.brevoKey == ""
	noTelegramKeys := f.tgToken == "" || f.tgChatID == ""
	return noEmailKeys && noTelegramKeys
}

// Slack credential modes, in selection order.
const (
	// slackModeBot uses chat.postMessage with a bot token: the only mode
	// that can address an arbitrary channel.
	slackModeBot = "bot"
	// slackModeWebhook posts to an incoming webhook URL supplied by env.
	slackModeWebhook = "webhook"
	// slackModeOp resolves the incoming webhook from 1Password.
	slackModeOp = "op-webhook"
	// slackModeUnconfigured means no Slack credential is available.
	slackModeUnconfigured = "unconfigured"
)

// slackSendTimeout bounds the whole Slack dispatch including retries.
const slackSendTimeout = 30 * time.Second

// slackMode picks the credential path. A bot token wins because it is the
// only transport that honors --slack-channel; the 1Password resolution is
// last because it is the slowest and the most failure-prone.
//
// notifyFlags is passed by pointer here and in the other v18776 helpers:
// the struct is ~250 bytes and these run on the send path.
func slackMode(f *notifyFlags) string {
	switch {
	case f.slackToken != "":
		return slackModeBot
	case f.slackWebhook != "":
		return slackModeWebhook
	case f.opToken != "":
		return slackModeOp
	default:
		return slackModeUnconfigured
	}
}

// populateSlackAudit adds slack fields to the audit map and performs the
// live send unless --dry-run was given. v18776.
func populateSlackAudit(audit map[string]any, opts *notifyOptions) {
	f := &opts.flags
	mode := slackMode(f)
	audit["slack_mode"] = mode
	audit["slack_channel"] = f.slackChannel
	audit["slack_bot_token_set"] = f.slackToken != ""
	audit["slack_webhook_set"] = f.slackWebhook != ""

	if mode == slackModeUnconfigured {
		audit["slack_blocker"] = "no Slack credential in env: set SLACK_BOT_TOKEN (bot mode), " +
			"SENTRUX_SLACK_WEBHOOK (webhook mode), or OP_SERVICE_ACCOUNT_TOKEN (1Password webhook mode)"
		return
	}
	if f.dryRun {
		audit["slack_result"] = "dry-run"
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), slackSendTimeout)
	defer cancel()
	if err := sendSlack(ctx, f, mode, slackText(f.subject, opts.bodyMD)); err != nil {
		audit["slack_result"] = "failed"
		// Redact defensively: the transport already scrubs its own token,
		// but a webhook URL resolved elsewhere could still surface here.
		audit["slack_error"] = slack.Redact(err.Error(), f.slackToken, f.slackWebhook)
		return
	}
	audit["slack_result"] = "sent"
	if f.cost {
		audit["slack_cost_estimate_usd"] = slackCostEstimate(1)
	}
}

// sendSlack dispatches through the mode-appropriate transport.
func sendSlack(ctx context.Context, f *notifyFlags, mode, text string) error {
	switch mode {
	case slackModeBot:
		cl, err := slack.NewBot(slack.BotConfig{
			Token:   f.slackToken,
			Channel: f.slackChannel,
			BaseURL: f.slackAPIBase,
		})
		if err != nil {
			return err
		}
		return cl.Post(ctx, text)
	case slackModeWebhook:
		return slack.NewFromURL(f.slackWebhook).WithChannel(f.slackChannel).Send(ctx, text)
	case slackModeOp:
		cl, err := slack.NewFromOp(ctx, f.slackChannel)
		if err != nil {
			return err
		}
		return cl.Send(ctx, text)
	default:
		return fmt.Errorf("notify: unsupported slack mode %q", mode)
	}
}

// slackText renders the chat body. Reuses channels.SanitizeSummary so the
// Slack payload obeys the same control-character and length rules as every
// other chat surface.
func slackText(subject, body string) string {
	var b strings.Builder
	if subject != "" {
		b.WriteString("[" + subject + "]")
		if body != "" {
			b.WriteString("\n\n")
		}
	}
	b.WriteString(body)
	return channels.SanitizeSummary(b.String())
}

// slackCostEstimate returns the USD estimate for N Slack sends. The Slack
// API is free; this mirrors telegramCostEstimate so cost rows stay
// comparable across surfaces per ADR-0023.
func slackCostEstimate(sends int) float64 {
	return 0.0001 * float64(sends)
}

// telegramWithStrikes sends via Telegram with exponential backoff. Returns
// (attempts used, result label, error).
func telegramWithStrikes(tg *telegram.Client, subject, body string, maxAttempts int) (int, string, error) {
	text := "[" + subject + "]\n\n" + truncate(body, 3500) // Telegram 4096-char limit
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := tg.SendMessage(ctx, text)
		cancel()
		if err == nil {
			return attempt, "sent", nil
		}
		lastErr = err
		if attempt < maxAttempts {
			time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond) // 200ms, 400ms
		}
	}
	return maxAttempts, "fallback-to-email", lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}

// emailCostEstimate returns the USD estimate for an email dispatch. v17508-5.
// Resend free tier: $0 (free); Brevo scale-up: $0.0004/email.
func emailCostEstimate(resend, brevo bool) float64 {
	if resend {
		return 0.0
	}
	if brevo {
		return 0.0004
	}
	return 0.0
}

// telegramCostEstimate returns the USD estimate for N Telegram sends at the
// rate of $0.0001 per send (Bot API is free; this is the routing-cost estimate
// per ADR-0023 for parity with email).
func telegramCostEstimate(attempts int) float64 {
	return 0.0001 * float64(attempts)
}

// ensure strings is referenced (used elsewhere if needed)
var _ = strings.TrimSpace

// ensure notify package import is referenced.
var _ = notify.DefaultRecipients

// ensure metrics import is referenced.
var _ = metrics.NewRegistry

// ensure notifydb import is referenced.
var _ = notifydb.DefaultPath
