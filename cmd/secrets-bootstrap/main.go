// secrets-bootstrap — read 1Password secrets via op CLI.
//
// Usage:
//   secrets-bootstrap vault item field [--export VAR]
//
// Examples:
//   secrets-bootstrap Cursor_IronClaw "github pat" password
//   secrets-bootstrap Cursor_IronClaw "minimax-api-1" api-key --export MINIMAX_API_KEY
//
// This binary is a thin wrapper around `op read` that:
//   - never logs the secret (only writes to stdout / env)
//   - redacts tokens in any error message
//   - exits 0 on success, 1 on failure
//   - has a 5s timeout (op CLI occasionally hangs on desktop-app IPC)
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const version = "1.0.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	exportVar := flag.String("export", "", "also export the value to this env var")
	timeoutSec := flag.Int("timeout", 5, "timeout in seconds for the op CLI call")
	flag.Parse()

	if *showVersion {
		fmt.Printf("secrets-bootstrap %s\n", version)
		os.Exit(0)
	}

	args := flag.Args()
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: secrets-bootstrap <vault> <item> <field> [--export VAR]")
		os.Exit(2)
	}
	vault, item, field := args[0], args[1], args[2]

	token := os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "OP_SERVICE_ACCOUNT_TOKEN not set; cannot proceed (no plaintext secrets in this binary)")
		os.Exit(1)
	}

	ctx := exec.CommandContext
	_ = ctx // unused import guard
	cmd := exec.Command("op", "read", fmt.Sprintf("op://%s/%s/%s", vault, item, field))
	cmd.Env = append(os.Environ(), "OP_SERVICE_ACCOUNT_TOKEN="+token)
	// Wire timeout via channel
	done := make(chan error, 1)
	go func() {
		out, err := cmd.Output()
		if err != nil {
			done <- err
			return
		}
		fmt.Print(strings.TrimRight(string(out), "\n"))
		if *exportVar != "" {
			// Print "export FOO=bar" so caller can eval
			fmt.Printf("\nexport %s=%q", *exportVar, strings.TrimRight(string(out), "\n"))
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			fmt.Fprintln(os.Stderr, redact(fmt.Sprintf("op read failed: %v", err)))
			os.Exit(1)
		}
	case <-time.After(time.Duration(*timeoutSec) * time.Second):
		_ = cmd.Process.Kill()
		fmt.Fprintln(os.Stderr, redact(fmt.Sprintf("op read timed out after %ds (desktop-app IPC stuck? consider 1Password Go SDK)", *timeoutSec)))
		os.Exit(1)
	}
}

// redact replaces any 1Password service-account JWT prefix with [REDACTED].
func redact(s string) string {
	const prefix = "ops_eyJ"
	if idx := strings.Index(s, prefix); idx >= 0 {
		// Keep 60 chars of context after the prefix then [REDACTED]
		end := idx + 60
		if end > len(s) {
			end = len(s)
		}
		return s[:idx] + prefix + "[REDACTED]"
	}
	return s
}
