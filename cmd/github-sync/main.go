// Command github-sync syncs a source git repo to a target git repo using
// `git push --mirror` with safety guards. Used for mirroring cursor-global-kb
// from primary GitHub remote to a personal backup fork.
//
// Usage:
//
//	github-sync \
//	  --source https://github.com/nfsarch33/cursor-global-kb.git \
//	  --target https://github.com/<personal-fork>/cursor-global-kb.git \
//	  --branch main \
//	  [--dry-run]
//
// In --dry-run mode (DEFAULT when no --dry-run=false flag is set) the command
// emits a structured audit JSON event to stdout. Real network calls are
// skipped. This is the canonical pattern for op-blocked or sandbox-blocked
// operations: audit-trail the attempt, surface the dry-run result.
//
// v16714-4 deliverable. TDD: see main_test.go (3 unit tests + 1 integration
// test stub).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// PlanSyncConfig captures the configuration for a github-sync run.
type PlanSyncConfig struct {
	Source string // URL of the source git remote
	Target string // URL of the target git remote
	Branch string // branch to sync (default: main)
	DryRun bool   // when true (default), emit audit event but skip network calls
}

// parseSyncArgs parses the CLI flags and returns a validated config.
// Returns an error if any required flag is missing.
func parseSyncArgs(args []string) (*PlanSyncConfig, error) {
	fs := flag.NewFlagSet("github-sync", flag.ContinueOnError)
	source := fs.String("source", "", "URL of source git remote (required)")
	target := fs.String("target", "", "URL of target git remote (required)")
	branch := fs.String("branch", "main", "branch to sync (default: main)")
	dryRun := fs.Bool("dry-run", true, "dry-run mode (default true; pass --dry-run=false to disable)")

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("flag parse: %w", err)
	}

	if strings.TrimSpace(*source) == "" {
		return nil, errors.New("source flag is required")
	}
	if strings.TrimSpace(*target) == "" {
		return nil, errors.New("target flag is required")
	}
	if strings.TrimSpace(*branch) == "" {
		return nil, errors.New("branch flag is required")
	}

	return &PlanSyncConfig{
		Source: *source,
		Target: *target,
		Branch: *branch,
		DryRun: *dryRun,
	}, nil
}

// buildSyncEvent creates a structured NDJSON event for audit logging.
// Used by both dry-run and real-sync paths.
func buildSyncEvent(cfg *PlanSyncConfig, result, detail string) map[string]any {
	return map[string]any{
		"event":   "github_sync_attempt",
		"source":  cfg.Source,
		"target":  cfg.Target,
		"branch":  cfg.Branch,
		"dry_run": cfg.DryRun,
		"result":  result,
		"detail":  detail,
		"ts":      time.Now().UTC().Format(time.RFC3339),
	}
}

// emitEvent writes the event as a single line of NDJSON to stdout.
func emitEvent(ev map[string]any) error {
	out, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	fmt.Fprintln(os.Stdout, string(out))
	return nil
}

func main() {
	os.Exit(runMain(os.Args[1:]))
}

// runMain is the testable entry point for github-sync.
func runMain(argv []string) int {
	cfg, err := parseSyncArgs(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "github-sync: %v\n", err)
		return 2
	}

	if cfg.DryRun {
		ev := buildSyncEvent(cfg, "dry-run", "no-op: --dry-run=true (default)")
		if err := emitEvent(ev); err != nil {
			fmt.Fprintf(os.Stderr, "github-sync: %v\n", err)
			return 1
		}
		return 0
	}

	// Real sync path (NOT IMPLEMENTED in v16714; deferred to v16718 closeout).
	// Will use `git push --mirror` with --force-with-lease (NEVER --force).
	// Operator pre-approval required per git-ops-guard.mdc.
	ev := buildSyncEvent(cfg, "skipped", "real sync path not yet implemented; v16718 closeout work")
	if err := emitEvent(ev); err != nil {
		fmt.Fprintf(os.Stderr, "github-sync: %v\n", err)
		return 1
	}
	return 0
}
