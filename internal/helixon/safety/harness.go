package safety

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrHarnessMaxIterations = errors.New("harness: max iterations exceeded")
	ErrHarnessBudget        = errors.New("harness: token budget exhausted")
	ErrHarnessTimeout       = errors.New("harness: execution timeout")
	ErrHarnessCostLimit     = errors.New("harness: cost limit exceeded")
)

// HarnessConstraints defines the execution limits for an agent run.
type HarnessConstraints struct {
	MaxIterations   int
	MaxTokensIn     int
	MaxTokensOut    int
	MaxCostUSD      float64
	Timeout         time.Duration
}

// DefaultConstraints returns sensible production defaults.
func DefaultConstraints() HarnessConstraints {
	return HarnessConstraints{
		MaxIterations:   50,
		MaxTokensIn:     500_000,
		MaxTokensOut:    200_000,
		MaxCostUSD:      5.0,
		Timeout:         10 * time.Minute,
	}
}

// HarnessState tracks the running state of an agent execution for constraint checking.
type HarnessState struct {
	mu          sync.Mutex
	constraints HarnessConstraints
	iterations  int
	tokensIn    int
	tokensOut   int
	costUSD     float64
	startedAt   time.Time
}

// NewHarnessState creates a harness enforcer with the given constraints.
func NewHarnessState(c HarnessConstraints) *HarnessState {
	return &HarnessState{
		constraints: c,
		startedAt:   time.Now(),
	}
}

// RecordIteration increments the iteration counter and records token usage.
// Returns an error if any constraint is violated.
func (h *HarnessState) RecordIteration(tokensIn, tokensOut int, costUSD float64) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.iterations++
	h.tokensIn += tokensIn
	h.tokensOut += tokensOut
	h.costUSD += costUSD

	if h.constraints.MaxIterations > 0 && h.iterations > h.constraints.MaxIterations {
		return fmt.Errorf("%w: %d/%d", ErrHarnessMaxIterations, h.iterations, h.constraints.MaxIterations)
	}
	if h.constraints.MaxTokensIn > 0 && h.tokensIn > h.constraints.MaxTokensIn {
		return fmt.Errorf("%w: input tokens %d/%d", ErrHarnessBudget, h.tokensIn, h.constraints.MaxTokensIn)
	}
	if h.constraints.MaxTokensOut > 0 && h.tokensOut > h.constraints.MaxTokensOut {
		return fmt.Errorf("%w: output tokens %d/%d", ErrHarnessBudget, h.tokensOut, h.constraints.MaxTokensOut)
	}
	if h.constraints.MaxCostUSD > 0 && h.costUSD > h.constraints.MaxCostUSD {
		return fmt.Errorf("%w: $%.4f/$%.2f", ErrHarnessCostLimit, h.costUSD, h.constraints.MaxCostUSD)
	}
	return nil
}

// CheckTimeout returns an error if the harness has exceeded its timeout.
func (h *HarnessState) CheckTimeout() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.constraints.Timeout <= 0 {
		return nil
	}
	if time.Since(h.startedAt) > h.constraints.Timeout {
		return fmt.Errorf("%w: %s elapsed, limit %s", ErrHarnessTimeout,
			time.Since(h.startedAt).Round(time.Second), h.constraints.Timeout)
	}
	return nil
}

// WithTimeout returns a context that will be cancelled when the harness timeout is reached.
func (h *HarnessState) WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	h.mu.Lock()
	timeout := h.constraints.Timeout
	started := h.startedAt
	h.mu.Unlock()

	if timeout <= 0 {
		return ctx, func() {}
	}

	remaining := timeout - time.Since(started)
	if remaining <= 0 {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx, cancel
	}
	return context.WithTimeout(ctx, remaining)
}

// Summary returns a snapshot of the current harness state.
func (h *HarnessState) Summary() HarnessSummary {
	h.mu.Lock()
	defer h.mu.Unlock()

	return HarnessSummary{
		Iterations: h.iterations,
		TokensIn:   h.tokensIn,
		TokensOut:  h.tokensOut,
		CostUSD:    h.costUSD,
		Elapsed:    time.Since(h.startedAt),
		Limits:     h.constraints,
	}
}

// HarnessSummary is a point-in-time snapshot of harness state.
type HarnessSummary struct {
	Iterations int              `json:"iterations"`
	TokensIn   int              `json:"tokens_in"`
	TokensOut  int              `json:"tokens_out"`
	CostUSD    float64          `json:"cost_usd"`
	Elapsed    time.Duration    `json:"elapsed"`
	Limits     HarnessConstraints `json:"limits"`
}
