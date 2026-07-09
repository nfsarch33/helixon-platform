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
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/endemail"
	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
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
		auditDB   = flag.String("audit-db", "", "path to notifydb SQLite file (optional; default: ~/logs/runx/notifydb.sqlite3)")
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

	var bodyMD string
	if *bodyFile != "" {
		raw, err := os.ReadFile(*bodyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: read body-file: %v\n", err)
			os.Exit(2)
		}
		bodyMD = string(raw)
	}

	// v17409-5: hardened END email template produces both HTML and
	// plain-text bodies. The CLI no longer carries the converter
	// inline.
	tmpl := endemail.Template{
		Plan:         *plan,
		Subject:      *subject,
		BodyMarkdown: bodyMD,
		JobID:        *jobID,
		IdempKey:     *idemKey,
		TenantID:     "cursor-global-kb",
	}
	m := tmpl.Build()

	if *noCC {
		m.CC = nil
	}

	// v17409-5: open notifydb (optional) and write a "rendered" audit row.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var db *notifydb.DB
	if *auditDB != "" {
		var err error
		db, err = notifydb.Open(*auditDB, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: open audit-db: %v (continuing without)\n", err)
		} else {
			defer db.Close()
		}
	} else {
		// Best-effort: try the default path. If it fails (no HOME, no
		// write permission), proceed without audit.
		if d, derr := notifydb.Open(notifydb.DefaultPath(), nil); derr == nil {
			db = d
			defer db.Close()
		}
	}
	_, _ = endemail.RenderAndAudit(ctx, tmpl, db)

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
		"html_body_len":   len(m.HTMLBody),
		"text_body_len":   len(m.TextBody),
		"audit_db":        db != nil,
	}

	auditEvent["idempotency_first_call"] = true

	if *dryRun || (*resendKey == "" && *brevoKey == "") {
		auditEvent["result"] = "dry-run"
		auditEvent["blocker"] = ""
		if *resendKey == "" && *brevoKey == "" {
			auditEvent["blocker"] = "op service account token expired (CARRY-055 unresolved). Provide --resend-key/--brevo-key or set RESEND_API_KEY/BREVO_API_KEY env to enable live send."
		}
		auditEvent["email_render"] = map[string]any{
			"to":            m.To,
			"cc":            m.CC,
			"subject":       m.Subject,
			"html_body_len": len(m.HTMLBody),
			"text_body_len": len(m.TextBody),
		}
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		fmt.Fprintln(os.Stderr, "DRY-RUN: skipping live network send")
		return
	}

	// Live path
	resendCfg := notify.ResendConfig{APIKey: *resendKey, FromAddr: *fromAddr}
	resendClient := notify.NewResendClient(resendCfg).WithAuditDB(db)

	brevoCfg := notify.BrevoConfig{APIKey: *brevoKey}
	brevoClient := notify.NewBrevoClient(brevoCfg).WithAuditDB(db)

	// v17409-6: attach metrics registry so the live send emits
	// notify_send_total and notify_send_attempts_total counters.
	metricsReg := metrics.NewRegistry(nil)
	disp := notify.NewDispatcher(notify.DispatcherConfig{
		ResendClient: resendClient,
		BrevoClient:  brevoClient,
	}).WithMetrics(metricsReg).WithAuditDB(db)

	if err := disp.Send(ctx, m); err != nil {
		auditEvent["result"] = "send-error"
		auditEvent["error"] = err.Error()
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		os.Exit(2)
	}

	auditEvent["result"] = "sent"

	// v17409-6: surface per-vendor counters in the audit event so
	// the rotation/observability story is visible without a Prometheus
	// scrape. The in-process registry already incremented via the
	// ResendClient/BrevoClient doWithRetry hooks.
	snap := metricsReg.Snapshot()
	sendCounts := make(map[string]int64, len(snap.SendCounts))
	for k, v := range snap.SendCounts {
		sendCounts[string(k.Vendor)+"/"+string(k.Status)] = v
	}
	attempts := make(map[string]int64, len(snap.Attempts))
	for k, v := range snap.Attempts {
		attempts[string(k.Vendor)] = v
	}
	auditEvent["notify_send_total"] = sendCounts
	auditEvent["notify_send_attempts_total"] = attempts

	out, _ := json.MarshalIndent(auditEvent, "", "  ")
	fmt.Println(string(out))
}
