package main

import (
	"context"
	"strings"
	"testing"
)

// v16716-3 RED tests for newTaskCmd refactor.
// newTaskCmd.RunE (CC=21) is decomposed into:
//   - setupTaskRuntime       (CC=4): config + provider + rt.Init
//   - claimAndBuildPrompt    (CC=4): sprintboard claim + default prompt
//   - executeAndReport       (CC=5): handle message + complete ticket
//   - runTaskPipeline        (CC=3): orchestrator
// newTaskCmd.RunE itself becomes a thin wrapper.

func TestRunTaskPipeline_RequiresTicketOrPrompt(t *testing.T) {
	t.Parallel()
	deps := taskDeps{}
	err := runTaskPipeline(context.Background(), taskArgs{}, deps, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "either --ticket or --prompt is required") {
		t.Fatalf("expected prompt-required error, got %v", err)
	}
}

func TestClaimAndBuildPrompt_DefaultsPromptWhenEmpty(t *testing.T) {
	t.Parallel()
	got := claimAndBuildPrompt("T-1", "")
	if !strings.Contains(got, "Execute SprintBoard ticket T-1") {
		t.Fatalf("default prompt should mention ticket: %q", got)
	}
}

func TestClaimAndBuildPrompt_PreservesExplicitPrompt(t *testing.T) {
	t.Parallel()
	got := claimAndBuildPrompt("T-1", "do this exact thing")
	if got != "do this exact thing" {
		t.Fatalf("explicit prompt overwritten: %q", got)
	}
}

func TestBuildSprintboardClient_NilWhenURLEmpty(t *testing.T) {
	t.Parallel()
	got := buildSprintboardClient("", "agent-x")
	if got != nil {
		t.Fatalf("expected nil client for empty URL, got %+v", got)
	}
}

func TestBuildSprintboardClient_BuiltWhenURLSet(t *testing.T) {
	t.Parallel()
	got := buildSprintboardClient("http://localhost:8787", "agent-x")
	if got == nil {
		t.Fatal("expected non-nil client when URL set")
	}
}
