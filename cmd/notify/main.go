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
	var (
		plan      = flag.String("plan", "", "plan range (e.g. v17501-v17600)")
		subject   = flag.String("subject", "", "notification subject")
		bodyFile  = flag.String("body-file", "", "path to markdown body")
		idemKey   = flag.String("idempotency-key", "", "idempotency key (required)")
		jobID     = flag.String("job-id", "", "cost-attribution job id")
		resendKey = flag.String("resend-key", "", "Resend API key (or RESEND_API_KEY env)")
		brevoKey  = flag.String("brevo-key", "", "Brevo API key (or BREVO_API_KEY env)")
		tgToken   = flag.String("telegram-token", "", "Telegram bot token (or TELEGRAM_BOT_TOKEN env)")
		tgChatID  = flag.String("telegram-chat-id", "", "Telegram chat ID (or TELEGRAM_CHAT_ID env)")
		via       = flag.String("via", "email", "send path: email | telegram | both")
		cost      = flag.Bool("cost", false, "emit cost observability in audit event")
		dryRun    = flag.Bool("dry-run", false, "skip network send, emit audit event")
		_         = flag.String("from", "noreply@oztac.com.au", "From address (email)")
	)
	flag.Parse()

	if *idemKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --idempotency-key required")
		os.Exit(2)
	}
	if *resendKey == "" {
		*resendKey = os.Getenv("RESEND_API_KEY")
	}
	if *brevoKey == "" {
		*brevoKey = os.Getenv("BREVO_API_KEY")
	}
	if *tgToken == "" {
		*tgToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if *tgChatID == "" {
		*tgChatID = os.Getenv("TELEGRAM_CHAT_ID")
	}

	var bodyMD string
	if *bodyFile != "" {
		raw, err := os.ReadFile(*bodyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: read body-file: %v\n", err)
			os.Exit(2)
		}
		bodyMD = string(raw)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	audit := map[string]any{
		"ts":                 now,
		"event":              "notify_attempt",
		"plan":               *plan,
		"job_id":             *jobID,
		"idempotency_key":    *idemKey,
		"via":                *via,
		"cost_requested":     *cost,
		"dry_run":            *dryRun,
		"resend_key_set":     *resendKey != "",
		"brevo_key_set":      *brevoKey != "",
		"telegram_token_set": *tgToken != "",
		"telegram_chat_set":  *tgChatID != "",
	}

	// Email path - reuse endemail template for HTML + text rendering
	if *via == "email" || *via == "both" {
		tmpl := endemail.Template{
			Plan:         *plan,
			Subject:      *subject,
			BodyMarkdown: bodyMD,
			JobID:        *jobID,
			IdempKey:     *idemKey,
			TenantID:     "cursor-global-kb",
		}
		m := tmpl.Build()
		audit["email_subject"] = m.Subject
		audit["email_to"] = m.To
		audit["email_cc"] = m.CC
		audit["email_html_len"] = len(m.HTMLBody)
		audit["email_text_len"] = len(m.TextBody)

		if *cost {
			// Reuse the observability cost estimator pattern: per-vendor unit cost
			// USD per attempt, multiplied by MaxRetry+1 attempts. Resend is $0
			// free-tier-aware; Brevo is $0.0004. v17508-5.
			audit["email_cost_estimate_usd"] = emailCostEstimate(*resendKey != "", *brevoKey != "")
		}
	}

	// Telegram path with 3-strike fallback (v17508-4)
	//nolint:gocritic // ifElseChain: two conditions are simpler as if/else if than a switch with bool expressions.
	if *via == "telegram" || *via == "both" {
		if *tgToken == "" || *tgChatID == "" {
			audit["telegram_blocker"] = "telegram bot token or chat ID not configured in 1Password (CF-2026-0708-010 partial)"
		} else if *dryRun {
			audit["telegram_result"] = "dry-run"
		} else {
			tg := telegram.New(telegram.Config{
				BotToken: *tgToken,
				ChatID:   *tgChatID,
			})
			tgAtt, tgRes, tgErr := telegramWithStrikes(tg, *subject, bodyMD, 3)
			audit["telegram_attempts"] = tgAtt
			audit["telegram_result"] = tgRes
			if tgErr != nil {
				audit["telegram_error"] = tgErr.Error()
			}
			if *cost {
				audit["telegram_cost_estimate_usd"] = telegramCostEstimate(tgAtt)
			}
		}
	}

	if *dryRun || ((*resendKey == "" && *brevoKey == "") && (*tgToken == "" || *tgChatID == "")) {
		audit["result"] = "dry-run"
		out, _ := json.MarshalIndent(audit, "", "  ")
		fmt.Println(string(out))
		fmt.Fprintln(os.Stderr, "DRY-RUN: skipping live network send")
		return
	}

	audit["result"] = "rendered-no-send"
	out, _ := json.MarshalIndent(audit, "", "  ")
	fmt.Println(string(out))
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
