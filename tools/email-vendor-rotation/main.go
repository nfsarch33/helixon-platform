// Package main implements the email-vendor-rotation tool for v16301.
//
// Strategy (per ADR-0087 + v16301 operator directive):
//   - Primary: Resend HTTP API
//   - Fallback: Brevo HTTP API
//   - Skip: SMTP2GO (quota exhausted; ADR-0087 forbids SMTP)
//   - Unified recipient: jaslian@gmail.com (no CC for live send)
//
// Selection algorithm: scan the rotation config, skip any key that was used
// in the last rotate_after_seconds window OR is in throttled/demoted/retired
// state. Pick the first active key of the primary vendor. If none, fall back
// to fallback vendor. If neither, return a structured error.
//
// Secrets are read on demand from 1Password via the `op` CLI. The tool never
// accepts API keys on argv; only alias names and message body.
//
// Usage:
//
//	email-vendor-rotation send \
//	  --config tools/email-vendor-rotation/config.yaml \
//	  --subject "..." --body-file body.md \
//	  --idempotency-key <key> --job-id <id> [--dry-run]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
)

type vendorKey struct {
	Alias      string `yaml:"alias"`
	Vault      string `yaml:"vault"`
	ItemID     string `yaml:"item_id"`
	Field      string `yaml:"field"`
	Vendor     string `yaml:"vendor"`
	Status     string `yaml:"status"`
	Notes      string `yaml:"notes"`
	LastUsedAt string `yaml:"last_used_at"`
}

// rotation is loaded from a YAML config. We hand-roll a tiny parser to keep
// the tool stdlib-only.
type parsedConfig struct {
	primary     string
	fallback    string
	forbidden   map[string]bool
	recipients  parsedRecipients
	from        map[string]string
	rotateAfter time.Duration
	keys        []vendorKey
}

type parsedRecipients struct {
	primary string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: email-vendor-rotation send|list")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "send":
		sendCmd(os.Args[2:])
	case "list":
		listCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func listCmd(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	cfgPath := fs.String("config", "tools/email-vendor-rotation/config.yaml", "path to rotation config")
	fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(2)
	}
	out, _ := json.MarshalIndent(cfg.keys, "", "  ")
	fmt.Println(string(out))
}

func sendCmd(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	cfgPath := fs.String("config", "tools/email-vendor-rotation/config.yaml", "path to rotation config")
	subject := fs.String("subject", "", "email subject")
	bodyFile := fs.String("body-file", "", "path to body file (markdown -> HTML)")
	idemKey := fs.String("idempotency-key", "", "idempotency key (required)")
	jobID := fs.String("job-id", "", "cost-attribution job id")
	dryRun := fs.Bool("dry-run", false, "skip live send; emit audit event")
	fs.Parse(args)

	if *idemKey == "" {
		fmt.Fprintln(os.Stderr, "--idempotency-key required")
		os.Exit(2)
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(2)
	}

	pick := selectKey(cfg, cfg.primary)
	if pick == nil {
		pick = selectKey(cfg, cfg.fallback)
	}
	if pick == nil {
		fmt.Fprintln(os.Stderr, "no active vendor key available (all demoted/expired/throttled)")
		os.Exit(3)
	}

	var htmlBody string
	if *bodyFile != "" {
		raw, err := os.ReadFile(*bodyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read body-file: %v\n", err)
			os.Exit(2)
		}
		htmlBody = string(raw) // markdown rendered to HTML by upstream
	}

	fromAddr := cfg.from[pick.Vendor]
	if fromAddr == "" {
		fromAddr = "helixon@resend.dev"
	}

	m := notify.Email{
		To:             []string{cfg.recipients.primary},
		CC:             nil,
		Subject:        *subject,
		HTMLBody:       htmlBody,
		TextBody:       "",
		IdempotencyKey: *idemKey,
		JobID:          *jobID,
		TenantID:       "cursor-global-kb",
		CostEstimate:   0.001,
	}

	auditEvent := map[string]any{
		"ts":              time.Now().UTC().Format(time.RFC3339),
		"event":           "email_vendor_rotation_attempt",
		"vendor":          pick.Vendor,
		"alias":           pick.Alias,
		"item_id":         pick.ItemID,
		"recipients":      m.To,
		"idempotency_key": *idemKey,
		"job_id":          *jobID,
		"from":            fromAddr,
		"dry_run":         *dryRun,
	}

	if *dryRun {
		auditEvent["result"] = "dry-run"
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		return
	}

	apiKey, err := readSecret(pick.Vault, pick.ItemID, pick.Field)
	if err != nil {
		auditEvent["result"] = "key-read-failed"
		auditEvent["error"] = err.Error()
		out, _ := json.MarshalIndent(auditEvent, "", "  ")
		fmt.Println(string(out))
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var client notify.Client
	switch pick.Vendor {
	case "resend":
		client = notify.NewResendClient(notify.ResendConfig{APIKey: apiKey, FromAddr: fromAddr})
	case "brevo":
		client = notify.NewBrevoClient(notify.BrevoConfig{APIKey: apiKey})
	default:
		fmt.Fprintf(os.Stderr, "unsupported vendor: %s\n", pick.Vendor)
		//nolint:gocritic // exitAfterDefer: cancel is on parent context; os.Exit is correct for unsupported vendor.
		os.Exit(3)
	}

	if err := client.Send(ctx, m); err != nil {
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

// selectKey returns the active key for the vendor with the oldest LastUsedAt
// (true LRU) that is also outside the cooldown window. If multiple keys have
// never been used (LastUsedAt == ""), the first such key in declaration order
// is returned (deterministic; never-used keys are interchangeable).
//
// v16712-LRU-1 refactor: previous behaviour was order-based (first active key
// in config order). New behaviour is timestamp-based (true LRU).
func selectKey(cfg *parsedConfig, vendor string) *vendorKey {
	if cfg.forbidden[vendor] {
		return nil
	}
	now := time.Now()
	var best *vendorKey
	var bestTime time.Time
	bestNeverUsed := false
	for i, k := range cfg.keys {
		if k.Vendor != vendor || k.Status != "active" {
			continue
		}
		if k.LastUsedAt == "" {
			// Never-used key is preferred over any used key. Among
			// never-used keys, keep the first one (declaration order)
			// for determinism.
			if !bestNeverUsed {
				best = &cfg.keys[i]
				bestNeverUsed = true
			}
			continue
		}
		t, err := time.Parse(time.RFC3339, k.LastUsedAt)
		if err != nil {
			// Malformed timestamp — skip.
			continue
		}
		if now.Sub(t) < cfg.rotateAfter {
			// Within cooldown — skip.
			continue
		}
		if bestNeverUsed {
			// A used key can never beat a never-used candidate.
			continue
		}
		if best == nil || t.Before(bestTime) {
			best = &cfg.keys[i]
			bestTime = t
		}
	}
	return best
}

func readSecret(vault, itemID, field string) (string, error) {
	ref := fmt.Sprintf("op://%s/%s/%s", vault, itemID, field)
	out, err := exec.Command("op", "read", ref).Output() //nolint:gosec // G204 subprocess executes pinned tool path
	if err != nil {
		return "", fmt.Errorf("op read %s: %w", ref, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
