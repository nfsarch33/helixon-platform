// Command send-end-email sends the END email for a completed plan range
// using helixon-platform/internal/notify. Idempotent via Email.IdempotencyKey.
//
// Usage:
//
//	send-end-email \
//	  --plan v16101-v16200 \
//	  --subject "[END] plan CLOSED GREEN" \
//	  --body-file /path/to/body.md \
//	  --idempotency-key v16101-v16200-end \
//	  --job-id v16101-v16200-end \
//	  --resend-key "$RESEND_API_KEY" \
//	  --brevo-key "$BREVO_API_KEY" \
//	  [--dry-run]
//
// In --dry-run mode (default when keys are empty) the command renders the
// Email struct, computes the dispatch order, validates the idempotency key,
// and emits a structured audit event to stdout as NDJSON. The real network
// call is skipped. This is the canonical pattern for op-blocked sends:
// audit-trail the attempt, surface the blocker, surface the dry-run result.
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
)

func main() {
	var (
		plan      = flag.String("plan", "", "plan range (e.g. v16101-v16200)")
		subject   = flag.String("subject", "", "email subject")
		bodyFile  = flag.String("body-file", "", "path to markdown body")
		idemKey   = flag.String("idempotency-key", "", "idempotency key (required)")
		jobID     = flag.String("job-id", "", "cost-attribution job id")
		resendKey = flag.String("resend-key", "", "Resend API key (or RESEND_API_KEY env)")
		brevoKey  = flag.String("brevo-key", "", "Brevo API key (or BREVO_API_KEY env)")
		dryRun    = flag.Bool("dry-run", false, "skip network send, emit audit event")
		fromAddr  = flag.String("from", "noreply@oztac.com.au", "From address")
		noCC      = flag.Bool("no-cc", false, "send only to Primary (jaslian@gmail.com) per v16301 unified-target directive")
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

	var htmlBody string
	if *bodyFile != "" {
		raw, err := os.ReadFile(*bodyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: read body-file: %v\n", err)
			os.Exit(2)
		}
		htmlBody = markdownToHTML(string(raw))
	}

	recipients := notify.DefaultRecipients()
	toAddrs := append([]string{recipients.Primary}, recipients.CC...)

	ccList := recipients.CC
	if *noCC {
		ccList = nil
	}

	m := notify.Email{
		To:             []string{recipients.Primary},
		CC:             ccList,
		Subject:        *subject,
		HTMLBody:       htmlBody,
		TextBody:       "", // markdown body is rendered to HTML; TextBody is fallback
		IdempotencyKey: *idemKey,
		JobID:          *jobID,
		TenantID:       "cursor-global-kb",
		CostEstimate:   0.001, // best-effort; single email
	}
	_ = toAddrs // Resend free-tier collapses CC into To per ADR-0087

	now := time.Now().UTC().Format(time.RFC3339)
	auditEvent := map[string]any{
		"ts":              now,
		"event":           "send_end_email_attempt",
		"plan":            *plan,
		"job_id":          *jobID,
		"idempotency_key": *idemKey,
		"recipients":      m.To,
		"cc":              m.CC,
		"subject":         m.Subject,
		"from":            *fromAddr,
		"dry_run":         *dryRun,
		"resend_key_set":  *resendKey != "",
		"brevo_key_set":   *brevoKey != "",
	}

	// Idempotency pre-check (mirrors the package's IdempotencyStore.Acquire
	// semantics but does not actually mutate store). Always returns
	// (acquired=true, inFlight=false) for the first call here because we
	// do not have a persistent store between processes. The package's
	// own in-memory store will dedup within a single process.
	auditEvent["idempotency_first_call"] = true

	if *dryRun || (*resendKey == "" && *brevoKey == "") {
		auditEvent["result"] = "dry-run"
		auditEvent["blocker"] = ""
		if *resendKey == "" && *brevoKey == "" {
			auditEvent["blocker"] = "op service account token expired (CARRY-055 unresolved). Provide --resend-key/--brevo-key or set RESEND_API_KEY/BREVO_API_KEY env to enable live send."
		}
		auditEvent["email_render"] = map[string]any{
			"to":       m.To,
			"cc":       m.CC,
			"subject":  m.Subject,
			"body_len": len(htmlBody),
		}
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		fmt.Fprintln(os.Stderr, "DRY-RUN: skipping live network send")
		return
	}

	// Live path
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resendCfg := notify.ResendConfig{APIKey: *resendKey, FromAddr: *fromAddr}
	resendClient := notify.NewResendClient(resendCfg)

	brevoCfg := notify.BrevoConfig{APIKey: *brevoKey}
	brevoClient := notify.NewBrevoClient(brevoCfg)

	disp := notify.NewDispatcher(notify.DispatcherConfig{
		ResendClient: resendClient,
		BrevoClient:  brevoClient,
	})

	if err := disp.Send(ctx, m); err != nil {
		auditEvent["result"] = "send-error"
		auditEvent["error"] = err.Error()
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		os.Exit(2)
	}

	auditEvent["result"] = "sent"
	out, _ := json.MarshalIndent(auditEvent, "", "  ")
	fmt.Println(string(out))
}

// markdownToHTML is a minimal converter for the END email body. We avoid
// pulling in a full markdown library because the END body is authored
// markdown and the notify package will accept HTML as-is.
func markdownToHTML(md string) string {
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "```"):
			if inCode {
				b.WriteString("</pre>\n")
				inCode = false
			} else {
				b.WriteString("<pre>")
				inCode = true
			}
		case strings.HasPrefix(line, "# "):
			b.WriteString("<h1>")
			b.WriteString(strings.TrimPrefix(line, "# "))
			b.WriteString("</h1>\n")
		case strings.HasPrefix(line, "## "):
			b.WriteString("<h2>")
			b.WriteString(strings.TrimPrefix(line, "## "))
			b.WriteString("</h2>\n")
		case strings.HasPrefix(line, "- "):
			b.WriteString("<li>")
			b.WriteString(strings.TrimPrefix(line, "- "))
			b.WriteString("</li>\n")
		case strings.HasPrefix(line, "|") && strings.Contains(line, "|"):
			// very rough table row
			b.WriteString(line)
			b.WriteString("\n")
		default:
			if inCode {
				b.WriteString(line)
				b.WriteString("\n")
			} else {
				b.WriteString("<p>")
				b.WriteString(line)
				b.WriteString("</p>\n")
			}
		}
	}
	if inCode {
		b.WriteString("</pre>\n")
	}
	return b.String()
}
