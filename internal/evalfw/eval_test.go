package evalfw

import (
	"context"
	"testing"
	"time"
)

func TestNewRunner_DefaultConfig(t *testing.T) {
	t.Parallel()
	r := NewRunner(RunnerConfig{})
	if r == nil {
		t.Fatal("NewRunner returned nil")
	}
	if r.config.Timeout <= 0 {
		t.Fatalf("expected positive default timeout, got %v", r.config.Timeout)
	}
}

func TestRunner_RunSuite_EmptySuite(t *testing.T) {
	t.Parallel()
	r := NewRunner(RunnerConfig{Timeout: 5 * time.Second})
	result, err := r.RunSuite(context.Background(), Suite{
		Name:  "empty",
		Cases: nil,
	})
	if err != nil {
		t.Fatalf("RunSuite on empty suite: %v", err)
	}
	if result.TotalCases != 0 {
		t.Fatalf("expected 0 cases, got %d", result.TotalCases)
	}
	if result.Verdict != VerdictPass {
		t.Fatalf("expected PASS verdict for empty suite, got %s", result.Verdict)
	}
}

func TestRunner_RunSuite_SinglePassCase(t *testing.T) {
	t.Parallel()
	r := NewRunner(RunnerConfig{Timeout: 5 * time.Second})
	result, err := r.RunSuite(context.Background(), Suite{
		Name: "single-pass",
		Cases: []Case{
			{
				Name: "always-pass",
				Fn: func(ctx context.Context) CaseResult {
					return CaseResult{Verdict: VerdictPass, Metrics: map[string]float64{"latency_ms": 1.5}}
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if result.TotalCases != 1 {
		t.Fatalf("expected 1 case, got %d", result.TotalCases)
	}
	if result.Passed != 1 {
		t.Fatalf("expected 1 passed, got %d", result.Passed)
	}
	if result.Verdict != VerdictPass {
		t.Fatalf("expected PASS, got %s", result.Verdict)
	}
}

func TestRunner_RunSuite_FailCase(t *testing.T) {
	t.Parallel()
	r := NewRunner(RunnerConfig{Timeout: 5 * time.Second})
	result, err := r.RunSuite(context.Background(), Suite{
		Name: "with-fail",
		Cases: []Case{
			{
				Name: "pass-case",
				Fn: func(ctx context.Context) CaseResult {
					return CaseResult{Verdict: VerdictPass}
				},
			},
			{
				Name: "fail-case",
				Fn: func(ctx context.Context) CaseResult {
					return CaseResult{Verdict: VerdictFail, Error: "expected X got Y"}
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if result.TotalCases != 2 {
		t.Fatalf("expected 2 cases, got %d", result.TotalCases)
	}
	if result.Failed != 1 {
		t.Fatalf("expected 1 failed, got %d", result.Failed)
	}
	if result.Verdict != VerdictFail {
		t.Fatalf("expected FAIL verdict, got %s", result.Verdict)
	}
}

func TestRunner_RunSuite_WarnCase(t *testing.T) {
	t.Parallel()
	r := NewRunner(RunnerConfig{Timeout: 5 * time.Second})
	result, err := r.RunSuite(context.Background(), Suite{
		Name: "with-warn",
		Cases: []Case{
			{
				Name: "warn-case",
				Fn: func(ctx context.Context) CaseResult {
					return CaseResult{Verdict: VerdictWarn, Error: "latency above threshold"}
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if result.Warned != 1 {
		t.Fatalf("expected 1 warned, got %d", result.Warned)
	}
	if result.Verdict != VerdictWarn {
		t.Fatalf("expected WARN verdict, got %s", result.Verdict)
	}
}

func TestRunner_RunSuite_Timeout(t *testing.T) {
	t.Parallel()
	r := NewRunner(RunnerConfig{Timeout: 50 * time.Millisecond})
	result, err := r.RunSuite(context.Background(), Suite{
		Name: "slow-suite",
		Cases: []Case{
			{
				Name: "slow-case",
				Fn: func(ctx context.Context) CaseResult {
					select {
					case <-ctx.Done():
						return CaseResult{Verdict: VerdictFail, Error: "timeout"}
					case <-time.After(5 * time.Second):
						return CaseResult{Verdict: VerdictPass}
					}
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected 1 failed (timeout), got %d failed", result.Failed)
	}
}
