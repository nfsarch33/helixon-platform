package agent

import (
	"context"
	"errors"
	"testing"
)

// TestCheckRunTermination_Timeout returns ErrTimeout when ctx is done.
func TestCheckRunTermination_Timeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &RunResult{}
	if err := checkRunTermination(ctx, r, 0, 100, 10); !errors.Is(err, ErrTimeout) {
		t.Errorf("got %v, want ErrTimeout", err)
	}
	if r.Err == nil {
		t.Errorf("RunResult.Err should be set")
	}
}

// TestCheckRunTermination_BudgetExhausted returns ErrBudgetExhaust when
// the in/out token sum is greater than MaxTokens.
func TestCheckRunTermination_BudgetExhausted(t *testing.T) {
	t.Parallel()
	r := &RunResult{TokensIn: 60, TokensOut: 50}
	if err := checkRunTermination(context.Background(), r, 0, 100, 10); !errors.Is(err, ErrBudgetExhaust) {
		t.Errorf("got %v, want ErrBudgetExhaust", err)
	}
}

// TestCheckRunTermination_OK returns nil when there is room left.
func TestCheckRunTermination_OK(t *testing.T) {
	t.Parallel()
	r := &RunResult{TokensIn: 10, TokensOut: 20}
	if err := checkRunTermination(context.Background(), r, 0, 100, 10); err != nil {
		t.Errorf("got %v, want nil", err)
	}
}

// TestFinalize_NoToolCalls sets FinalContent and returns final=true.
func TestFinalize_NoToolCalls(t *testing.T) {
	t.Parallel()
	r := &RunResult{}
	final, err := finalizeRun(r, "done", 0)
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if !final {
		t.Errorf("expected final=true (no tool calls)")
	}
	if r.FinalContent != "done" {
		t.Errorf("FinalContent: got %q want 'done'", r.FinalContent)
	}
}

// TestFinalize_WithToolCalls returns final=false so the orchestrator continues.
func TestFinalize_WithToolCalls(t *testing.T) {
	t.Parallel()
	r := &RunResult{}
	final, err := finalizeRun(r, "ignored", 2)
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if final {
		t.Errorf("expected final=false (tool calls present)")
	}
	if r.FinalContent != "" {
		t.Errorf("FinalContent should be empty when tool calls are pending; got %q", r.FinalContent)
	}
}
