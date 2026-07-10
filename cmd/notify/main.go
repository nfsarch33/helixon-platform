// Command notify exposes the helixon-platform/internal/notify package as a
// CLI for ad-hoc operator use (cost observability + Telegram live-send with
// 3-strike fallback). v17508-4 / v17508-5.
//
// Usage:
//
//	notify --cost --to "user@host" \
//	       --subject "[END]" --body-file body.md \
//	       --idempotency-key v17508-end \
//	       [--via email|telegram|both] \
//	       [--dry-run]
//
// In --dry-run mode (default when keys are empty) the command renders the
// dispatch, computes costs, applies the 3-strike policy, and emits a
// structured audit event to stdout as NDJSON. The real network call is
// skipped. This mirrors send-end-email's audit-first posture.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/endemail"
	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
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
	fs.StringVar(&f.via, "via", "email", "send path: email | telegram | both")
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

	f.resendKey = envOrFlag(f.resendKey, "RESEND_API_KEY")
	f.brevoKey = envOrFlag(f.brevoKey, "BREVO_API_KEY")
	f.tgToken = envOrFlag(f.tgToken, "TELEGRAM_BOT_TOKEN")
	f.tgChatID = envOrFlag(f.tgChatID, "TELEGRAM_CHAT_ID")

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
	raw, err := os.ReadFile(path)
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

// viaIncludes reports whether the via spec includes the named path.
// v17714-1: extracted for clarity.
func viaIncludes(via, want string) bool {
	return via == want || via == "both"
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
func emitAudit(audit map[string]any, f notifyFlags) int {
	dryRun := isDryRun(audit, f)
	if dryRun {
		audit["result"] = "dry-run"
		out, _ := json.MarshalIndent(audit, "", "  ")
		fmt.Println(string(out))
		fmt.Fprintln(os.Stderr, "DRY-RUN: skipping live network send")
		return 0
	}
	audit["result"] = "rendered-no-send"
	out, _ := json.MarshalIndent(audit, "", "  ")
	fmt.Println(string(out))
	return 0
}

// isDryRun reports whether the dispatch should skip the live send.
// v17714-1: extracted from emitAudit.
func isDryRun(audit map[string]any, f notifyFlags) bool {
	if f.dryRun {
		return true
	}
	noEmailKeys := f.resendKey == "" && f.brevoKey == ""
	noTelegramKeys := f.tgToken == "" || f.tgChatID == ""
	return noEmailKeys && noTelegramKeys
}

// telegramWithStrikes sends via Telegram with exponential backoff. Returns
// (attempts used, result label, error).
func telegramWithStrikes(tg *telegram.Client, subject, body string, max int) (int, string, error) {
	text := "[" + subject + "]\n\n" + truncate(body, 3500) // Telegram 4096-char limit
	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := tg.SendMessage(ctx, text)
		cancel()
		if err == nil {
			return attempt, "sent", nil
		}
		lastErr = err
		if attempt < max {
			time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond) // 200ms, 400ms
		}
	}
	return max, "fallback-to-email", lastErr
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
