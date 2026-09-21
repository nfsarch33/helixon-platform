// Package tooldispatch TestFileGuard (v18857, §3.2 Q2a): blocks writes to
// Go test files while a ticket run is in flight.
//
// The scoring contract treats tests as the ruler: an agent scored on a
// ticket must not be able to touch the ruler. The guard denies file_write
// calls whose target ends in _test.go, and ONLY while at least one ticket
// run holds the guard — human-driven sessions keep full write freedom.
//
// Boundary, deliberately: shell-mediated writes to test files are NOT
// intercepted here. Command-line analysis is a heuristic that would both
// miss real evasion and false-positive on legitimate test RUNNING; the
// shell jail (v18783) bounds what shell can do at all, and run evidence
// carries the git diff where any test-file tampering stays visible to the
// reviewer. Extending the guard to shell is a follow-up only if a
// scored run is caught evading through it.
package tooldispatch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// ErrTestFileWriteDenied is returned to the loop when a ticket-run agent
// tries to write a *_test.go file. A sentinel so callers (loop policy,
// observability) can tell a policy denial from a tool failure.
var ErrTestFileWriteDenied = errors.New("test file write denied: tests are the contract and are read-only during scored runs")

// TestFileGuard tracks how many ticket runs are in flight. The count (not
// a bool) is what makes MaxConcurrent > 1 safe: the last run out releases
// the restriction, not the first.
type TestFileGuard struct {
	inflight atomic.Int32
}

// Enter marks one ticket run as in flight.
func (g *TestFileGuard) Enter() { g.inflight.Add(1) }

// Leave marks one ticket run as finished. Callers must Leave exactly once
// per Enter; runTicket's defer is the single pairing site.
func (g *TestFileGuard) Leave() { g.inflight.Add(-1) }

// Active reports whether any ticket run holds the guard.
func (g *TestFileGuard) Active() bool { return g.inflight.Load() > 0 }

// TestFileGuardExecutor wraps an InnerExecutor with the Q2a write guard.
type TestFileGuardExecutor struct {
	inner  InnerExecutor
	guard  *TestFileGuard
	logger *slog.Logger
}

// NewTestFileGuardExecutor wraps inner. A nil guard disables the
// decorator entirely (forward-only), which keeps non-file-tool runtimes
// and tests untouched.
func NewTestFileGuardExecutor(inner InnerExecutor, guard *TestFileGuard, logger *slog.Logger) *TestFileGuardExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &TestFileGuardExecutor{inner: inner, guard: guard, logger: logger.With(slog.String("component", "helixon.tooldispatch.testfileguard"))}
}

// Execute denies file_write to *_test.go while the guard is active;
// everything else forwards unchanged. Malformed args forward too: a
// missing path is the file_write handler's own error to report, and a
// guard that choked on JSON would mask the tool's diagnostics.
func (g *TestFileGuardExecutor) Execute(ctx context.Context, name string, argsJSON string) (string, error) { //nolint:revive // unused-parameter required by interface
	if g.guard != nil && g.guard.Active() && name == "file_write" {
		if path := pathArg(argsJSON); strings.HasSuffix(path, "_test.go") {
			g.logger.Warn("test-file write denied during ticket run",
				slog.String("tool", name),
				slog.String("path", path),
			)
			return "", errors.Join(ErrTestFileWriteDenied, errors.New("path: "+path))
		}
	}
	return g.inner.Execute(ctx, name, argsJSON)
}

// Available proxies through to the inner executor.
func (g *TestFileGuardExecutor) Available() []llm.Tool { return g.inner.Available() }

// pathArg pulls the "path" string out of a tool-call args blob. Empty on
// any miss; the write tool rejects an empty path itself.
func pathArg(argsJSON string) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	return args.Path
}
