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

// TestCheckRunTermination_BudgetWinsWhenBothLimitsCrossed pins the decision
// order (v18801). Under load a run can be over budget AND past its deadline at
// the same check. The verdict must be ErrBudgetExhaust: retry classification
// treats a timeout as retryable and a blown budget as terminal, so reporting
// ErrTimeout here re-runs — and re-pays — a run that policy already stopped.
func TestCheckRunTermination_BudgetWinsWhenBothLimitsCrossed(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &RunResult{TokensIn: 60, TokensOut: 50}
	if err := checkRunTermination(ctx, r, 0, 100, 10); !errors.Is(err, ErrBudgetExhaust) {
		t.Errorf("got %v, want ErrBudgetExhaust when both limits are crossed", err)
	}
	if !errors.Is(r.Err, ErrBudgetExhaust) {
		t.Errorf("RunResult.Err should record budget exhaustion, got %v", r.Err)
	}
}

// TestCheckTokenBudget_Boundary pins the boundary the contract states: the
// budget is exhausted when the token sum is GREATER than MaxTokens, so
// spending exactly the budget is still inside it.
//
// It is here because nothing else in the package exercised that edge — every
// other case sits far from the limit — so swapping the comparison for >= was
// an off-by-one that passed the whole suite. A limit test that never tests the
// limit is decoration.
func TestCheckTokenBudget_Boundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		in, out, max  int
		wantExhausted bool
	}{
		{"well under budget", 10, 20, 100, false},
		{"one token short", 60, 39, 100, false},
		{"exactly at budget is not exhausted", 60, 40, 100, false},
		{"one token over is exhausted", 60, 41, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &RunResult{TokensIn: tc.in, TokensOut: tc.out}
			err := checkTokenBudget(r, tc.max)
			if tc.wantExhausted {
				if !errors.Is(err, ErrBudgetExhaust) {
					t.Errorf("%d+%d against %d: got %v, want ErrBudgetExhaust", tc.in, tc.out, tc.max, err)
				}
				if !errors.Is(r.Err, ErrBudgetExhaust) {
					t.Errorf("RunResult.Err should record the verdict, got %v", r.Err)
				}
				return
			}
			if err != nil {
				t.Errorf("%d+%d against %d: got %v, want nil", tc.in, tc.out, tc.max, err)
			}
			if r.Err != nil {
				t.Errorf("RunResult.Err should stay clean, got %v", r.Err)
			}
		})
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
