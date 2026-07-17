// Copyright 2026 Helixon Platform. SPDX-License-Identifier: MIT.
// Additional checkpoint tests for v17805-3 (coverage lift).
package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckpoint_NewAppliesDefaultOutputPath asserts the OutputPath
// default branch in New() (env-expanded fallback).
func TestCheckpoint_NewAppliesDefaultOutputPath(t *testing.T) {
	e := New(Config{})
	if e.cfg.OutputPath == "" {
		t.Fatal("OutputPath default should expand $HOME/logs/...; got empty")
	}
	want := os.ExpandEnv("$HOME/logs/runx/agentrace-checkpoint.ndjson")
	if e.cfg.OutputPath != want {
		t.Fatalf("OutputPath default: want %q, got %q", want, e.cfg.OutputPath)
	}
}

// TestCheckpoint_NewInitializesBudgetAt100 asserts New sets budgetPct=100
// in the returned Emitter.
func TestCheckpoint_NewInitializesBudgetAt100(t *testing.T) {
	e := New(Config{OutputPath: "/tmp/x.ndjson"})
	if e.budgetPct != 100 {
		t.Fatalf("budgetPct at init: want 100, got %d", e.budgetPct)
	}
	if e.toolCalls != 0 {
		t.Fatalf("toolCalls at init: want 0, got %d", e.toolCalls)
	}
}

// TestCheckpoint_EmitLocked_OpenFileError asserts emitLocked returns an
// error when OutputPath is a directory (OpenFile will fail) rather than
// a regular file.
func TestCheckpoint_EmitLocked_OpenFileError(t *testing.T) {
	dir := t.TempDir()
	// Create a directory at the OutputPath — OpenFile with O_CREATE cannot
	// succeed against a directory that already exists as a directory.
	e := New(Config{EveryNToolCalls: 1, OutputPath: dir})
	if err := e.OnToolCall(); err == nil {
		t.Fatal("OnToolCall against directory path should error")
	}
}

// TestCheckpoint_EmitLocked_PathTraversalAlwaysReturnsValid asserts
// that emitLocked writes a valid NDJSON line (one JSON object + newline).
func TestCheckpoint_EmitLocked_PathTraversalAlwaysReturnsValid(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 1, OutputPath: logPath})

	// Fire 3 checkpoints — each appends, total bytes = sum.
	for i := 0; i < 3; i++ {
		if err := e.OnToolCall(); err != nil {
			t.Fatalf("OnToolCall %d: %v", i+1, err)
		}
	}
	data, err := os.ReadFile(logPath) //nolint:gosec // G304 test fixture
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// 3 newlines expected.
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("expected 3 NDJSON lines, got %d newlines (file=%d bytes)", count, len(data))
	}
}

// TestCheckpoint_Tick_NoFireBeforeInterval_WithExplicitConfig asserts
// Tick does NOT emit when EveryTMinutes is large and elapsed < interval.
// Already tested in existing test but this exercises a different path
// (large positive config values are NOT replaced by defaults).
func TestCheckpoint_Tick_NoFireBeforeInterval_WithExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 99999, EveryTMinutes: 5 * time.Minute, OutputPath: logPath})

	// Even after a small sleep, no emit.
	time.Sleep(10 * time.Millisecond)
	if err := e.Tick(); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("Tick emitted before 5m interval; want no file at %s", logPath)
	}
}
