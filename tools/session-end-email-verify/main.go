// Command session-end-email-verify is the v16711-5 self-test for the
// session-end-email-notify.sh hook script. It exercises the
// send-end-email CLI end-to-end with safe inputs and emits a structured
// audit event so the operator can verify the wire.
//
// Usage:
//
//	session-end-email-verify [--dry-run] [--plan v16710] [--sentrux-score 6944]
//
// Exit codes:
//
//	0 = verified (dry-run OR live-send-ok)
//	1 = failure (config missing, parse error, dispatch error)
//
// This binary deliberately stays minimal: it does NOT own the email-send
// path (that's cmd/send-end-email); it only validates that the surrounding
// hook script + binary pair works.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", true, "emit audit only; do not actually send")
	plan := fs.String("plan", "v16710", "plan range label for audit event")
	sentruxScore := fs.Int("sentrux-score", 6944, "current helixon-platform Sentrux Q score")
	fs.Parse(os.Args[1:])

	binaryPath := os.Getenv("NOTIFY_FALLBACK")
	if binaryPath == "" {
		binaryPath = os.Getenv("HOME") + "/Code/helixon-platform/bin/send-end-email"
	}

	// Sanity: do not call network in dry-run (--dry-run is forwarded).
	args := []string{
		"--plan", *plan,
		"--subject", fmt.Sprintf("[END] plan v16711-verify %s", *plan),
		"--body-file", os.Getenv("HOME") + "/Code/cursor-global-kb/reports/research/v16600-end-email-body.md",
		"--idempotency-key", "v16711-verify-" + fmt.Sprintf("%d", time.Now().Unix()),
		"--job-id", *plan + "-verify",
	}
	if *dryRun {
		args = append(args, "--dry-run")
	}

	auditEvent := map[string]any{
		"ts":            time.Now().UTC().Format(time.RFC3339),
		"event":         "session_end_email_verify",
		"plan":          *plan,
		"dry_run":       *dryRun,
		"binary_path":   binaryPath,
		"sentrux_score": *sentruxScore,
		"args":          args,
	}

	if _, err := os.Stat(binaryPath); err != nil {
		auditEvent["result"] = "binary_missing"
		auditEvent["error"] = err.Error()
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		os.Exit(2)
	}

	cmd := exec.Command(binaryPath, args...)
	cmd.Env = os.Environ() // inherit; binary will refuse to send without keys
	out, err := cmd.CombinedOutput()
	if err != nil {
		auditEvent["result"] = "verify_failed"
		auditEvent["error"] = err.Error()
		auditEvent["output"] = string(out)
		auditJSON, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(auditJSON))
		os.Exit(1)
	}

	auditEvent["result"] = "verified"
	auditEvent["output_tail"] = tail(string(out), 1000)
	auditJSON, _ := json.MarshalIndent(auditEvent, "", "  ")
	fmt.Println(string(auditJSON))
}

func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}
