// Package tooldispatch LoopGuard integration: wraps any InnerExecutor with a
// loop detector that returns ErrLoopDetected (from internal/loopguard) when
// a tool-call hash is repeated beyond the configured threshold within the
// sliding window.
//
// The hash is computed as `name + "|" + argsJSON` — collisions on distinct
// arguments are acceptable for the MVP-1 use case (the agent runner decides
// what to do with ErrLoopDetected; typically break the loop).
//
// Author/Machine-Id: cursor-parent@win3-wsl3
package tooldispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
)

// LoopGuardExecutor wraps an InnerExecutor with loop detection.
type LoopGuardExecutor struct {
	inner    InnerExecutor
	guard    *loopguard.LoopGuard
	onDetect func(toolName, hash string) // optional callback (metrics emit)
}

// NewLoopGuardExecutor wraps inner with the given LoopGuard (caller-owned).
func NewLoopGuardExecutor(inner InnerExecutor, guard *loopguard.LoopGuard) *LoopGuardExecutor {
	if guard == nil {
		guard = loopguard.New(loopguard.DefaultThreshold, loopguard.DefaultWindow)
	}
	return &LoopGuardExecutor{inner: inner, guard: guard}
}

// WithOnDetect sets a callback invoked when ErrLoopDetected fires.
// Useful for metrics + Agentrace emission (v17003-4).
func (g *LoopGuardExecutor) WithOnDetect(fn func(toolName, hash string)) *LoopGuardExecutor {
	g.onDetect = fn
	return g
}

// Execute hashes (name, argsJSON) and calls guard.Observe; on ErrLoopDetected
// it returns the error WITHOUT calling the inner executor (fail-fast).
// On any other outcome (including guard-allowed), it forwards to inner.
func (g *LoopGuardExecutor) Execute(ctx context.Context, name string, argsJSON string) (string, error) {
	hash := hashToolCall(name, argsJSON)
	if err := g.guard.Observe(hash); err != nil {
		if g.onDetect != nil {
			g.onDetect(name, hash)
		}
		return "", err
	}
	return g.inner.Execute(ctx, name, argsJSON)
}

// Available proxies through to the inner executor.
func (g *LoopGuardExecutor) Available() []llm.Tool {
	return g.inner.Available()
}

// Guard returns the underlying *loopguard.LoopGuard for tests + Stats().
func (g *LoopGuardExecutor) Guard() *loopguard.LoopGuard {
	return g.guard
}

// hashToolCall returns a stable short hash of (tool name + args).
// SHA-256 truncated to 16 hex chars (64-bit) is sufficient for MVP-1; the
// window + threshold logic does not require cryptographic strength.
func hashToolCall(name, argsJSON string) string {
	h := sha256.Sum256([]byte(name + "|" + argsJSON))
	return hex.EncodeToString(h[:8])
}
