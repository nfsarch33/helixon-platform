// Run identity on the wire (v18846-3): every model call of a durable run
// carries X-HLXN-Run-Id / X-HLXN-Ticket-Id / X-Helixon-Agent so the router
// can attribute student traffic without sniffing bodies or User-Agent.
// Deviation from agent-lightning (which binds identity by URL path):
// Helixon's base_url is per-process, so identity travels by header.
package llm

import (
	"context"
	"net/http"
)

// RunHeader names the run identifier header.
const RunHeader = "X-HLXN-Run-Id" //nolint:gosec // G101: header names, not credentials

// TicketHeader names the ticket identifier header (absent for non-ticket runs).
const TicketHeader = "X-HLXN-Ticket-Id"

// AgentHeader names the agent identity header.
const AgentHeader = "X-Helixon-Agent"

// RunInfo is the identity of the durable run a model call belongs to.
// TicketID is empty for non-ticket runs (REPL, tasks) and the decorator
// omits the header rather than sending an empty value.
type RunInfo struct {
	RunID    string
	TicketID string
	AgentID  string
}

type runInfoKey struct{}

// WithRunInfo attaches run identity to a context. Values survive derived
// contexts (timeouts, cancellation), which is how the run loop wraps them.
func WithRunInfo(ctx context.Context, info RunInfo) context.Context {
	return context.WithValue(ctx, runInfoKey{}, info)
}

// RunInfoFromContext returns the attached identity, or ok=false when the
// call has no run context (ordinary HTTP usage of the same client).
func RunInfoFromContext(ctx context.Context) (RunInfo, bool) {
	info, ok := ctx.Value(runInfoKey{}).(RunInfo)
	return info, ok
}

// IdentityDoer stamps run identity headers onto every request whose
// context carries RunInfo. It never reads or rewrites bodies.
type IdentityDoer struct {
	inner HTTPDoer
}

// NewIdentityDoer wraps an HTTPDoer; a nil inner panics on use, matching
// the callers' always-construct-a-client reality.
func NewIdentityDoer(inner HTTPDoer) *IdentityDoer {
	return &IdentityDoer{inner: inner}
}

// Do stamps identity headers when the request context carries RunInfo and
// forwards unchanged otherwise.
func (d *IdentityDoer) Do(req *http.Request) (*http.Response, error) {
	if info, ok := RunInfoFromContext(req.Context()); ok {
		if info.RunID != "" {
			req.Header.Set(RunHeader, info.RunID)
		}
		if info.TicketID != "" {
			req.Header.Set(TicketHeader, info.TicketID)
		}
		if info.AgentID != "" {
			req.Header.Set(AgentHeader, info.AgentID)
		}
	}
	return d.inner.Do(req)
}
