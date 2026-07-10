// Package headroom implements the v14515 headroom-enforcement layer
// described in docs/token-saving-strategy.md section 5.
//
// The hook decides whether the prompt + expected response will fit
// inside the cell's context window after accounting for system
// instructions, tool definitions, and reserved response space.
//
// Rejection is loud: a HeadroomError contains both the budget and the
// actual size so the caller (or operator) can fix the prompt rather
// than relying on silent truncation.
package headroom

import (
	"errors"
	"fmt"
)

// DefaultBudgets is the conservative token-budget table per cell.
var DefaultBudgets = map[string]Budget{
	"qwen36-27b-q4":       {Context: 32_000, ReservedResponse: 1024, ReservedSystem: 512},
	"qwen36-27b-q8":       {Context: 32_000, ReservedResponse: 2048, ReservedSystem: 512},
	"qwen36-27b-mtp":      {Context: 32_000, ReservedResponse: 2048, ReservedSystem: 512},
	"opus-class-remote":   {Context: 200_000, ReservedResponse: 4096, ReservedSystem: 1024},
	"sonnet-class-remote": {Context: 200_000, ReservedResponse: 4096, ReservedSystem: 1024},
	"local-echo":          {Context: 8_000, ReservedResponse: 256, ReservedSystem: 64},
}

// Budget describes a cell's token capacity.
type Budget struct {
	Context          int
	ReservedResponse int
	ReservedSystem   int
}

// Available returns the number of tokens available for prompt +
// tool output (Context - ReservedResponse - ReservedSystem).
func (b Budget) Available() int {
	return b.Context - b.ReservedResponse - b.ReservedSystem
}

// HeadroomError is returned by Check when the prompt would not fit.
type HeadroomError struct {
	CellID   string
	Budget   Budget
	Required int
}

func (e *HeadroomError) Error() string {
	return fmt.Sprintf(
		"headroom: cell=%s budget_available=%d required=%d (over by %d); shrink prompt or pick a larger cell",
		e.CellID, e.Budget.Available(), e.Required, e.Required-e.Budget.Available(),
	)
}

// Check returns nil if `required` tokens fit inside the cell's
// available headroom. Otherwise returns *HeadroomError.
func Check(cellID string, requiredTokens int) error {
	b, ok := DefaultBudgets[cellID]
	if !ok {
		// Unknown cell: fail safe with a 32k context.
		b = Budget{Context: 32_000, ReservedResponse: 1024, ReservedSystem: 512}
	}
	if requiredTokens <= 0 {
		return errors.New("headroom: requiredTokens must be positive")
	}
	if requiredTokens > b.Available() {
		return &HeadroomError{CellID: cellID, Budget: b, Required: requiredTokens}
	}
	return nil
}

// EstimateTokens is a rough heuristic: 1 token ≈ 4 chars of English
// text. Callers should prefer their model's actual tokenizer when
// available; this is the fallback used by the hook when no
// tokenizer is registered.
func EstimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	n := len(s) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// ForCell is a convenience wrapper that computes EstimateTokens
// and runs Check in one call.
func ForCell(cellID, prompt string) error {
	return Check(cellID, EstimateTokens(prompt))
}
