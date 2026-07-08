// Package main — github-sync TDD tests.
//
// TDD: these tests are written BEFORE the main.go implementation.
// They cover:
//   - parseSyncArgs: required flags validation
//   - PlanSyncConfig: dry-run default behaviour
//   - emitSyncEvent: structured NDJSON output
//
// Reference: cursor-global-kb/reports/research/v16714-cicd-fork-research.md
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSyncArgs_RequiredFlags(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "all flags present",
			args:    []string{"--source", "src.git", "--target", "tgt.git", "--branch", "main"},
			wantErr: false,
		},
		{
			name:      "missing source",
			args:      []string{"--target", "tgt.git", "--branch", "main"},
			wantErr:   true,
			errSubstr: "source",
		},
		{
			name:      "missing target",
			args:      []string{"--source", "src.git", "--branch", "main"},
			wantErr:   true,
			errSubstr: "target",
		},
		{
			name:    "branch defaults to main when not provided",
			args:    []string{"--source", "src.git", "--target", "tgt.git"},
			wantErr: false,
		},
		{
			name:    "dry-run flag works",
			args:    []string{"--source", "src.git", "--target", "tgt.git", "--branch", "main", "--dry-run"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSyncArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSyncArgs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("parseSyncArgs() error = %q, want substring %q", err.Error(), tt.errSubstr)
			}
		})
	}
}

func TestPlanSyncConfig_DryRunDefault(t *testing.T) {
	cfg, err := parseSyncArgs([]string{"--source", "src.git", "--target", "tgt.git", "--branch", "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.DryRun {
		t.Errorf("expected DryRun to default to true when --dry-run not passed, got false")
	}
	if cfg.Source != "src.git" {
		t.Errorf("Source = %q, want %q", cfg.Source, "src.git")
	}
	if cfg.Target != "tgt.git" {
		t.Errorf("Target = %q, want %q", cfg.Target, "tgt.git")
	}
	if cfg.Branch != "main" {
		t.Errorf("Branch = %q, want %q", cfg.Branch, "main")
	}
}

func TestEmitSyncEvent_StructuredJSON(t *testing.T) {
	cfg := &PlanSyncConfig{
		Source: "src.git",
		Target: "tgt.git",
		Branch: "main",
		DryRun: true,
	}
	ev := buildSyncEvent(cfg, "dry-run", "no-op")

	if ev["event"] != "github_sync_attempt" {
		t.Errorf("event = %v, want %q", ev["event"], "github_sync_attempt")
	}
	if ev["result"] != "dry-run" {
		t.Errorf("result = %v, want %q", ev["result"], "dry-run")
	}
	if ev["source"] != "src.git" {
		t.Errorf("source = %v, want %q", ev["source"], "src.git")
	}
	if ev["target"] != "tgt.git" {
		t.Errorf("target = %v, want %q", ev["target"], "tgt.git")
	}
	if ev["branch"] != "main" {
		t.Errorf("branch = %v, want %q", ev["branch"], "main")
	}
	if ev["detail"] != "no-op" {
		t.Errorf("detail = %v, want %q", ev["detail"], "no-op")
	}
	if _, ok := ev["ts"]; !ok {
		t.Error("event missing ts field")
	}

	// Verify the event can be JSON-encoded (used by emit to stdout)
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if parsed["event"] != "github_sync_attempt" {
		t.Errorf("round-trip lost event field")
	}
}
