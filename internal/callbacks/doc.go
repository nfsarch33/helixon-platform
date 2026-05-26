// Package callbacks provides a handler-based callback spine for agent
// component observability. Handlers receive OnStart, OnEnd, and OnError
// events with typed RunInfo metadata and can emit telemetry to any backend.
//
// Built-in handlers: NDJSONHandler (structured log files), PrometheusHandler
// (counters + histograms), MultiHandler (fan-out), NoopHandler (testing).
//
// This package bridges to github.com/cloudwego/eino/callbacks per ADR-052.
// It is retained as the internal handler surface; the ReAct agent's callback
// bridge (internal/uiauto/react/callbacks.go) forwards eino callback events
// into handlers registered here.
//
// The design is informed by the eino framework's callbacks concept
// (cloudwego/eino v0.9.0-alpha.9) as documented in the consolidated
// analysis at global-kb/reports/research/
// eino-consolidated-requirement-analysis-design-and-plan-2026-05-12.md §3.6.
//
// This is an independent implementation — no eino source was vendored or
// copied. See ADR-036 for the adoption rationale.
package callbacks
