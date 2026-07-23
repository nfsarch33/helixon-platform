// runx-public-repo-gate: allow-file personal_path_id
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
	"io"
	"os"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/endemail"
	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

func main() {
	os.Exit(runSendEndEmailCmd(os.Args[1:]))
}

// endEmailFlags holds parsed flags for the send-end-email command.
// v17714-1: extracted from main() to keep the dispatcher ≤6.
type endEmailFlags struct {
	plan      string
	subject   string
	bodyFile  string
	idemKey   string
	jobID     string
	resendKey string
	brevoKey  string
	dryRun    bool
	fromAddr  string
	noCC      bool
	auditDB   string
	brevoOnly bool // xcut-10 (v18518): CF-105 Resend domain unverified → Brevo only.
}

// endEmailOptions groups inputs for runSendEndEmailCmd.
type endEmailOptions struct {
	flags     endEmailFlags
	bodyMD    string
	timestamp string
}

// runSendEndEmailCmd is the testable entry point. v17714-1: extracted
// from main(); original was CC=18, now ≤6.
func runSendEndEmailCmd(args []string) int {
	opts, rc := parseEndEmailArgs(args)
	if rc != 0 {
		return rc
	}

	m := renderEmail(opts)
	if opts.flags.noCC {
		m.CC = nil
	}

	db, cleanup := openAuditDB(opts.flags.auditDB)
	if cleanup != nil {
		defer cleanup()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = endemail.RenderAndAudit(ctx, buildTemplate(opts), db)

	return dispatchEndEmail(ctx, opts, m, db)
}

// parseEndEmailArgs parses flags + env fallbacks + body file.
// v17714-1: extracted.
func parseEndEmailArgs(args []string) (endEmailOptions, int) {
	fs := flag.NewFlagSet("send-end-email", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var f endEmailFlags
	fs.StringVar(&f.plan, "plan", "", "plan range (e.g. v16101-v16200)")
	fs.StringVar(&f.subject, "subject", "", "email subject")
	fs.StringVar(&f.bodyFile, "body-file", "", "path to markdown body")
	fs.StringVar(&f.idemKey, "idempotency-key", "", "idempotency key (required)")
	fs.StringVar(&f.jobID, "job-id", "", "cost-attribution job id")
	fs.StringVar(&f.resendKey, "resend-key", "", "Resend API key (or RESEND_API_KEY env)")
	fs.StringVar(&f.brevoKey, "brevo-key", "", "Brevo API key (or BREVO_API_KEY env)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "skip network send, emit audit event")
	fs.StringVar(&f.fromAddr, "from", "noreply@oztac.com.au", "From address")
	fs.BoolVar(&f.noCC, "no-cc", false, "send only to Primary (jaslian@gmail.com) per v16301 unified-target directive")
	fs.StringVar(&f.auditDB, "audit-db", "", "path to notifydb SQLite file (optional; default: ~/logs/runx/notifydb.sqlite3)")
	// xcut-10 (v18518): when --brevo-only is set, Resend is excluded
	// from the dispatch. Defaults from env NOTIFY_BREVO_ONLY=1 too.
	brevoOnlyDefault := os.Getenv("NOTIFY_BREVO_ONLY") == "1"
	fs.BoolVar(&f.brevoOnly, "brevo-only", brevoOnlyDefault, "skip Resend entirely (CF-105 Resend domain unverified; xcut-10)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return endEmailOptions{}, 2
	}
	if f.idemKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --idempotency-key required")
		return endEmailOptions{}, 2
	}
	f.resendKey = envOrValue(f.resendKey, os.Getenv("RESEND_API_KEY"))
	f.brevoKey = envOrValue(f.brevoKey, os.Getenv("BREVO_API_KEY"))

	bodyMD, rc := readBodyFileOptional(f.bodyFile)
	if rc != 0 {
		return endEmailOptions{}, rc
	}

	return endEmailOptions{
		flags:     f,
		bodyMD:    bodyMD,
		timestamp: time.Now().UTC().Format(time.RFC3339),
	}, 0
}

// envOrValue returns existing value if non-empty, else fallback. v17714-1.
func envOrValue(existing, fallback string) string {
	if existing != "" {
		return existing
	}
	return fallback
}

// readBodyFileOptional reads the optional body file. v17714-1.
func readBodyFileOptional(path string) (string, int) {
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

// buildTemplate constructs the endemail.Template. v17714-1: extracted.
func buildTemplate(opts endEmailOptions) endemail.Template {
	f := opts.flags
	return endemail.Template{
		Plan:         f.plan,
		Subject:      f.subject,
		BodyMarkdown: opts.bodyMD,
		JobID:        f.jobID,
		IdempKey:     f.idemKey,
		TenantID:     "cursor-global-kb",
	}
}

// renderEmail builds the rendered Email struct. v17714-1: extracted.
func renderEmail(opts endEmailOptions) notify.Email {
	return buildTemplate(opts).Build()
}

// openAuditDB opens the audit-DB (or default) and returns db + cleanup.
// v17714-1: extracted.
func openAuditDB(auditDBPath string) (*notifydb.DB, func()) {
	if auditDBPath != "" {
		db, err := notifydb.Open(auditDBPath, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: open audit-db: %v (continuing without)\n", err)
			return nil, nil
		}
		return db, func() { _ = db.Close() }
	}
	// Best-effort: try the default path. If it fails (no HOME, no
	// write permission), proceed without audit.
	if d, derr := notifydb.Open(notifydb.DefaultPath(), nil); derr == nil {
		return d, func() { _ = d.Close() }
	}
	return nil, nil
}

// dispatchEndEmail routes the rendered email to dry-run or live send.
// v17714-1: extracted.
func dispatchEndEmail(ctx context.Context, opts endEmailOptions, m notify.Email, db *notifydb.DB) int {
	audit := buildEndAudit(opts, m, db != nil)
	if isEndDryRun(opts.flags) {
		markEndDryRun(audit, m, opts.flags.resendKey == "" && opts.flags.brevoKey == "")
		return printAuditAndExit(audit, 0, "DRY-RUN: skipping live network send")
	}
	return runLiveSend(ctx, audit, opts, m, db)
}

// buildEndAudit constructs the base audit event for send-end-email.
// v17714-1: extracted.
func buildEndAudit(opts endEmailOptions, m notify.Email, auditDBActive bool) map[string]any {
	f := opts.flags
	return map[string]any{
		"ts":                     opts.timestamp,
		"event":                  "send_end_email_attempt",
		"plan":                   f.plan,
		"job_id":                 f.jobID,
		"idempotency_key":        f.idemKey,
		"recipients":             m.To,
		"cc":                     m.CC,
		"subject":                m.Subject,
		"from":                   f.fromAddr,
		"dry_run":                f.dryRun,
		"resend_key_set":         f.resendKey != "",
		"brevo_key_set":          f.brevoKey != "",
		"html_body_len":          len(m.HTMLBody),
		"text_body_len":          len(m.TextBody),
		"audit_db":               auditDBActive,
		"idempotency_first_call": true,
	}
}

// isEndDryRun reports whether to skip the live send. v17714-1.
func isEndDryRun(f endEmailFlags) bool {
	if f.dryRun {
		return true
	}
	return f.resendKey == "" && f.brevoKey == ""
}

// markEndDryRun fills the dry-run-only fields of the audit map.
// v17714-1: extracted.
func markEndDryRun(audit map[string]any, m notify.Email, noKeys bool) {
	audit["result"] = "dry-run"
	audit["blocker"] = ""
	if noKeys {
		audit["blocker"] = "op service account token expired (CARRY-055 unresolved). Provide --resend-key/--brevo-key or set RESEND_API_KEY/BREVO_API_KEY env to enable live send."
	}
	audit["email_render"] = map[string]any{
		"to":            m.To,
		"cc":            m.CC,
		"subject":       m.Subject,
		"html_body_len": len(m.HTMLBody),
		"text_body_len": len(m.TextBody),
	}
}

// runLiveSend dispatches to the actual Resend/Brevo clients and returns
// the exit code. v17714-1: extracted.
func runLiveSend(ctx context.Context, audit map[string]any, opts endEmailOptions, m notify.Email, db *notifydb.DB) int {
	f := opts.flags
	resendCfg := notify.ResendConfig{APIKey: f.resendKey, FromAddr: f.fromAddr}
	resendClient := notify.NewResendClient(resendCfg).WithAuditDB(db)

	brevoCfg := notify.BrevoConfig{APIKey: f.brevoKey}
	brevoClient := notify.NewBrevoClient(brevoCfg).WithAuditDB(db)

	metricsReg := metrics.NewRegistry(nil)
	disp := notify.NewDispatcher(notify.DispatcherConfig{
		ResendClient: resendClient,
		BrevoClient:  brevoClient,
		BrevoOnly:    f.brevoOnly,
	}).WithMetrics(metricsReg).WithAuditDB(db)

	if err := disp.Send(ctx, m); err != nil {
		audit["result"] = "send-error"
		audit["error"] = err.Error()
		_ = printAuditJSON(audit)
		return 2
	}

	audit["result"] = "sent"
	attachMetrics(audit, metricsReg)
	_ = printAuditJSON(audit)
	return 0
}

// attachMetrics surfaces per-vendor counters in the audit event so the
// rotation/observability story is visible without a Prometheus scrape.
// v17714-1: extracted from main().
func attachMetrics(audit map[string]any, reg *metrics.Registry) {
	snap := reg.Snapshot()
	sendCounts := make(map[string]int64, len(snap.SendCounts))
	for k, v := range snap.SendCounts {
		sendCounts[string(k.Vendor)+"/"+string(k.Status)] = v
	}
	attempts := make(map[string]int64, len(snap.Attempts))
	for k, v := range snap.Attempts {
		attempts[string(k.Vendor)] = v
	}
	audit["notify_send_total"] = sendCounts
	audit["notify_send_attempts_total"] = attempts
}

// printAuditAndExit prints the audit JSON, an optional stderr marker,
// and returns the desired exit code. v17714-1.
func printAuditAndExit(audit map[string]any, rc int, stderrLine string) int {
	_ = printAuditJSON(audit)
	if stderrLine != "" {
		fmt.Fprintln(os.Stderr, stderrLine)
	}
	return rc
}

// printAuditJSON marshals the audit map and prints to stdout.
// v17714-1: extracted.
func printAuditJSON(audit map[string]any) error {
	out, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}
