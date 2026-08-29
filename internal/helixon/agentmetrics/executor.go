package agentmetrics

import (
	"context"
	"errors"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
)

// InnerExecutor is the executor surface this decorator wraps. It is declared
// here rather than imported from the agent package so that agent can stay free
// of any dependency on metrics.
type InnerExecutor interface {
	Execute(ctx context.Context, name, argsJSON string) (string, error)
	Available() []llm.Tool
}

// MeteredExecutor counts every tool dispatch.
//
// It is installed OUTERMOST in the decorator chain, on purpose. A refusal by
// the sandbox gate or the loop guard never reaches the registry, so a counter
// placed at the registry would report a clean run for an agent whose every call
// was being denied — the case an operator most needs to see.
//
// The `tool` label is bounded at construction by the set of registered tool
// names. A model is free to invent a name; this decorator is not free to invent
// a time series, so an unknown name is counted as ToolUnregistered.
type MeteredExecutor struct {
	inner   InnerExecutor
	metrics *Metrics
	known   map[string]struct{}
}

// NewMeteredExecutor wraps inner. The registered tool names are snapshotted
// from inner.Available(); tools registered after this point are counted under
// ToolUnregistered rather than being allowed to widen the label domain at
// runtime.
func NewMeteredExecutor(inner InnerExecutor, m *Metrics) *MeteredExecutor {
	known := make(map[string]struct{})
	names := []string{}
	if inner != nil {
		for _, t := range inner.Available() {
			known[t.Function.Name] = struct{}{}
			names = append(names, t.Function.Name)
		}
	}
	// Publish the zero series now: a tool that has never been called and a
	// tool that has never been denied are different facts, and only one of
	// them is visible if the series appears on first use.
	m.InitToolSeries(names)
	return &MeteredExecutor{inner: inner, metrics: m, known: known}
}

// Available proxies to the inner executor: counting calls does not change which
// tools the model is told about.
func (e *MeteredExecutor) Available() []llm.Tool { return e.inner.Available() }

// Execute forwards the call and records its outcome.
func (e *MeteredExecutor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
	out, err := e.inner.Execute(ctx, name, argsJSON)
	e.metrics.ToolCall(e.label(name), ClassifyToolError(err))
	return out, err
}

// label bounds the tool name to the registered set.
func (e *MeteredExecutor) label(name string) string {
	if _, ok := e.known[name]; ok {
		return name
	}
	return ToolUnregistered
}

// ClassifyToolError maps a dispatch error onto the frozen outcome domain.
//
// The distinction that matters is `denied` versus `error`: a guardrail refusing
// a call is the system working, and a tool blowing up is the system failing.
// Reporting both as `error` would make a correctly-contained agent look broken;
// reporting both as `denied` would hide real breakage behind the guardrails.
func ClassifyToolError(err error) string {
	switch {
	case err == nil:
		return ToolOK
	case errors.Is(err, sandbox.ErrToolDenied),
		errors.Is(err, sandbox.ErrHostExecutionRefused),
		errors.Is(err, loopguard.ErrLoopDetected):
		return ToolDenied
	default:
		return ToolError
	}
}
