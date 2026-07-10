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

	os.Exit(runVerify(verifyOptions{
		DryRun:       *dryRun,
		Plan:         *plan,
		SentruxScore: *sentruxScore,
	}))
}

// verifyOptions is the structured input for runVerify.
type verifyOptions struct {
	DryRun       bool
	Plan         string
	SentruxScore int
}

// runVerify performs the v16711-5 self-test and returns the exit code.
func runVerify(o verifyOptions) int {
	binaryPath := os.Getenv("NOTIFY_FALLBACK")
	if binaryPath == "" {
		binaryPath = os.Getenv("HOME") + "/Code/helixon-platform/bin/send-end-email"
	}
	// Sanity: do not call network in dry-run (--dry-run is forwarded).
	args := []string{
		"--plan", o.Plan,
		"--subject", fmt.Sprintf("[END] plan v16711-verify %s", o.Plan),
		"--body-file", os.Getenv("HOME") + "/Code/cursor-global-kb/reports/research/v16600-end-email-body.md",
		"--idempotency-key", "v16711-verify-" + fmt.Sprintf("%d", time.Now().Unix()),
		"--job-id", o.Plan + "-verify",
	}
	if o.DryRun {
		args = append(args, "--dry-run")
	}

	auditEvent := map[string]any{
		"ts":            time.Now().UTC().Format(time.RFC3339),
		"event":         "session_end_email_verify",
		"plan":          o.Plan,
		"dry_run":       o.DryRun,
		"binary_path":   binaryPath,
		"sentrux_score": o.SentruxScore,
		"args":          args,
	}

	if _, err := os.Stat(binaryPath); err != nil {
		auditEvent["result"] = "binary_missing"
		auditEvent["error"] = err.Error()
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		return 2
	}

	out, err := invokeBinary(binaryPath, args)
	if err != nil {
		auditEvent["result"] = "verify_failed"
		auditEvent["error"] = err.Error()
		auditEvent["output"] = string(out)
		auditJSON, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(auditJSON))
		return 1
	}

	auditEvent["result"] = "verified"
	auditEvent["output_tail"] = tail(string(out), 1000)
	auditJSON, _ := json.MarshalIndent(auditEvent, "", "  ")
	fmt.Println(string(auditJSON))
	return 0
}

// invokeBinary runs binaryPath with args, returning combined output and the
// non-nil error from CombinedOutput on failure. Extracted for testability.
func invokeBinary(binaryPath string, args []string) ([]byte, error) {
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}
