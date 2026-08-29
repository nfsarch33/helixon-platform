// Package agentmetrics owns the Prometheus contract for the Helixon agent
// runtime.
//
// The runtime already had a metrics *field* — platform.Config.PrometheusRegisterer
// — and nothing ever set it, so the agent that claims tickets and escalates
// them ran with no exported series at all. An escalation is the safety property
// the per-ticket reliability tier rests on, and until this package existed an
// escalation only mutated board state: no counter moved, no alert could fire,
// and no human was told.
//
// Two rules shape everything here:
//
//   - Names, types and label VALUES are frozen (see the v18784 metric
//     contract). Producers elsewhere in the tree hand this package a string;
//     this package is the single place that decides which strings become label
//     values, so a producer cannot widen the contract by inventing a new one.
//   - No label may carry unbounded cardinality. There are no ticket IDs, no
//     error strings and no filesystem paths in any label. The one label whose
//     domain is not a compile-time constant — `tool` — is bounded at the
//     decorator by the set of REGISTERED tool names, and anything outside that
//     set collapses to a single sentinel value.
//
// Every method is nil-receiver safe. Metrics are optional wiring: a caller
// that never built a *Metrics passes nil and the call sites stay unconditional,
// rather than growing an `if m != nil` at each of the fifteen places a counter
// is bumped (which is how one of them ends up missing).
package agentmetrics

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Escalation reasons. `verifier_failed` covers both verifier stop conditions:
// a repeatedly-failing check and a mutating run that produced no passing
// evidence. They are one operational fact — the gate refused to call this
// done — and splitting them would only make the alert harder to read.
const (
	ReasonVerifierFailed  = "verifier_failed"
	ReasonBudgetExhausted = "budget_exhausted"
	ReasonRunError        = "run_error"
)

// Verifier outcomes. `fail` is a check that ran and returned red; `error` is a
// check that never produced a verdict. Collapsing them would let a verifier
// that cannot start masquerade as a verifier that failed.
const (
	VerifierPass  = "pass"
	VerifierFail  = "fail"
	VerifierError = "error"
)

// Tool-call outcomes.
const (
	ToolOK     = "ok"
	ToolError  = "error"
	ToolDenied = "denied"
)

// Sandbox failure kinds. These mirror sandbox.FailureKind* one-for-one;
// AcceptedSandboxKinds is what a cross-package test asserts against so the two
// halves cannot drift apart silently.
const (
	SandboxPreflight = "preflight"
	SandboxTimeout   = "timeout"
	SandboxExec      = "exec"
)

// Run outcomes for the duration histogram. A run cut short by shutdown is
// neither: it is not observed at all, for the same reason the poller refuses to
// report one to the board.
const (
	RunCompleted = "completed"
	RunEscalated = "escalated"
)

// Token directions.
const (
	DirectionIn  = "in"
	DirectionOut = "out"
)

// ToolUnregistered is the sentinel `tool` label for a call the model made to a
// name that is not registered. Without it, a model that hallucinates tool names
// would mint one time series per hallucination.
const ToolUnregistered = "unregistered"

// acceptedEscalationReasons etc. are the closed label domains. A producer that
// hands over anything else is normalised to the safe fallback rather than
// creating a new series.
var (
	acceptedEscalationReasons = map[string]struct{}{
		ReasonVerifierFailed: {}, ReasonBudgetExhausted: {}, ReasonRunError: {},
	}
	acceptedVerifierOutcomes = map[string]struct{}{
		VerifierPass: {}, VerifierFail: {}, VerifierError: {},
	}
	acceptedToolOutcomes = map[string]struct{}{
		ToolOK: {}, ToolError: {}, ToolDenied: {},
	}
	acceptedSandboxKinds = map[string]struct{}{
		SandboxPreflight: {}, SandboxTimeout: {}, SandboxExec: {},
	}
	acceptedRunOutcomes = map[string]struct{}{
		RunCompleted: {}, RunEscalated: {},
	}
)

// AcceptedSandboxKinds returns the sandbox failure kinds this package will
// accept as label values. Exported so the sandbox package's own constants can
// be checked against it from a test, rather than the two sides each asserting
// their own copy of the string and both passing while disagreeing.
func AcceptedSandboxKinds() []string {
	return keys(acceptedSandboxKinds)
}

// AcceptedVerifierOutcomes returns the verifier outcome label values.
func AcceptedVerifierOutcomes() []string { return keys(acceptedVerifierOutcomes) }

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Metric names, frozen. Exported so the acceptance test asserts the same
// strings the collectors are built from, instead of a second copy that could
// agree with itself while disagreeing with the exposition.
const (
	NameTicketsClaimed   = "hlxn_agent_tickets_claimed_total"
	NameTicketsCompleted = "hlxn_agent_tickets_completed_total"
	NameEscalations      = "hlxn_agent_escalations_total"
	NameVerifierRuns     = "hlxn_agent_verifier_runs_total"
	NameLoopIterations   = "hlxn_agent_loop_iterations_total"
	NameToolCalls        = "hlxn_agent_tool_calls_total"
	NameSandboxFailures  = "hlxn_agent_sandbox_failures_total"
	// #nosec G101 -- a Prometheus metric name counting MODEL tokens, not a credential.
	NameTokens      = "hlxn_agent_tokens_total"
	NameRunDuration = "hlxn_agent_run_duration_seconds"
	NameBuildInfo   = "hlxn_agent_build_info"
)

// Names returns every metric name in the contract, in contract order.
func Names() []string {
	return []string{
		NameTicketsClaimed, NameTicketsCompleted, NameEscalations,
		NameVerifierRuns, NameLoopIterations, NameToolCalls,
		NameSandboxFailures, NameTokens, NameRunDuration, NameBuildInfo,
	}
}

// Metrics holds the agent-runtime collectors.
type Metrics struct {
	ticketsClaimed   prometheus.Counter
	ticketsCompleted prometheus.Counter
	escalations      *prometheus.CounterVec
	verifierRuns     *prometheus.CounterVec
	loopIterations   prometheus.Counter
	toolCalls        *prometheus.CounterVec
	sandboxFailures  *prometheus.CounterVec
	tokens           *prometheus.CounterVec
	runDuration      *prometheus.HistogramVec
	buildInfo        *prometheus.GaugeVec
}

// New builds the collectors, registers them with reg, and publishes revision as
// hlxn_agent_build_info.
//
// The revision is a constructor argument rather than a later setter on purpose.
// build_info is the series the absence alert keys on: if it could be forgotten,
// then forgetting it would make a perfectly healthy agent indistinguishable
// from a dead one, forever, with no other symptom.
//
// It uses Register rather than MustRegister deliberately: a duplicate
// registration is a wiring bug the caller should see as an error at start-up,
// not a panic in the middle of a serve loop.
func New(reg prometheus.Registerer, revision string) (*Metrics, error) {
	if reg == nil {
		return nil, fmt.Errorf("agentmetrics: a registerer is required")
	}
	m := &Metrics{
		ticketsClaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: NameTicketsClaimed,
			Help: "Tickets this agent claimed from the board.",
		}),
		ticketsCompleted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: NameTicketsCompleted,
			Help: "Tickets completed with evidence accepted by the board.",
		}),
		escalations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: NameEscalations,
			Help: "Tickets escalated to a human and deliberately NOT completed.",
		}, []string{"reason"}),
		verifierRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: NameVerifierRuns,
			Help: "verifier_run invocations by verdict.",
		}, []string{"outcome"}),
		loopIterations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: NameLoopIterations,
			Help: "Agent loop iterations across all runs.",
		}),
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: NameToolCalls,
			Help: "Tool dispatches by registered tool name and outcome.",
		}, []string{"tool", "outcome"}),
		sandboxFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: NameSandboxFailures,
			Help: "Sandboxed commands that never produced a verdict, by kind.",
		}, []string{"kind"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: NameTokens,
			Help: "Model tokens consumed by the agent loop, by direction.",
		}, []string{"direction"}),
		runDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: NameRunDuration,
			Help: "Wall-clock seconds per ticket run, by terminal outcome.",
			// A ticket run is minutes, not milliseconds: the default buckets
			// stop at 10s and would put every real run in +Inf.
			Buckets: prometheus.ExponentialBuckets(1, 2, 12),
		}, []string{"outcome"}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: NameBuildInfo,
			Help: "Always 1; the revision label identifies the running build.",
		}, []string{"revision"}),
	}
	for _, c := range m.collectors() {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("agentmetrics: register: %w", err)
		}
	}
	m.initSeries()
	m.setBuildInfo(revision)
	return m, nil
}

func (m *Metrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.ticketsClaimed, m.ticketsCompleted, m.escalations, m.verifierRuns,
		m.loopIterations, m.toolCalls, m.sandboxFailures, m.tokens,
		m.runDuration, m.buildInfo,
	}
}

// initSeries materializes every series whose label domain is known up front.
//
// This is not cosmetic. `absent()` and `== 0` are different questions, and an
// alert written against a counter that has never been touched cannot tell "no
// escalations happened" from "the agent is not running". Creating the zero
// series makes the first answer available immediately, and leaves absence to
// mean only the second.
func (m *Metrics) initSeries() {
	for r := range acceptedEscalationReasons {
		m.escalations.WithLabelValues(r)
	}
	for o := range acceptedVerifierOutcomes {
		m.verifierRuns.WithLabelValues(o)
	}
	for k := range acceptedSandboxKinds {
		m.sandboxFailures.WithLabelValues(k)
	}
	for o := range acceptedRunOutcomes {
		m.runDuration.WithLabelValues(o)
	}
	m.tokens.WithLabelValues(DirectionIn)
	m.tokens.WithLabelValues(DirectionOut)
	// The `tool` domain is not known here, but the family must exist or a
	// fresh scrape would omit hlxn_agent_tool_calls_total entirely — and the
	// acceptance criterion is that every contract name appears. The sentinel
	// is a real question with a real answer: have there been calls to tools
	// that are not registered?
	m.InitToolSeries(nil)
}

// InitToolSeries materializes the zero series for a known set of tool names.
//
// Called once the registered tools are known (see MeteredExecutor), so an
// operator can tell "shell has never been denied" from "shell has never been
// called" — and so a rate() over a tool that has been quiet all day returns 0
// instead of nothing.
func (m *Metrics) InitToolSeries(tools []string) {
	if m == nil {
		return
	}
	for _, tool := range append([]string{ToolUnregistered}, tools...) {
		for outcome := range acceptedToolOutcomes {
			m.toolCalls.WithLabelValues(tool, outcome)
		}
	}
}

// setBuildInfo publishes the running revision. An empty revision is recorded as
// "unknown" rather than as an empty label, so a scrape can always distinguish
// "this build did not stamp a revision" from "no agent is running".
func (m *Metrics) setBuildInfo(revision string) {
	if revision == "" {
		revision = "unknown"
	}
	m.buildInfo.WithLabelValues(revision).Set(1)
}

// TicketClaimed records one successful claim.
func (m *Metrics) TicketClaimed() {
	if m == nil {
		return
	}
	m.ticketsClaimed.Inc()
}

// TicketCompleted records one ticket the board accepted as done.
func (m *Metrics) TicketCompleted() {
	if m == nil {
		return
	}
	m.ticketsCompleted.Inc()
}

// Escalated records one ticket handed back to a human. An unrecognized reason
// is normalised to run_error rather than minting a new series.
func (m *Metrics) Escalated(reason string) {
	if m == nil {
		return
	}
	m.escalations.WithLabelValues(normalise(reason, acceptedEscalationReasons, ReasonRunError)).Inc()
}

// VerifierRun records one verifier_run invocation.
func (m *Metrics) VerifierRun(outcome string) {
	if m == nil {
		return
	}
	m.verifierRuns.WithLabelValues(normalise(outcome, acceptedVerifierOutcomes, VerifierError)).Inc()
}

// ObserveLoopIteration records one agent loop iteration. The name matches the
// agent.RunObserver interface, which is where this is called from.
func (m *Metrics) ObserveLoopIteration() {
	if m == nil {
		return
	}
	m.loopIterations.Inc()
}

// ObserveTokens records the real per-iteration token usage reported by the
// provider.
func (m *Metrics) ObserveTokens(promptTokens, completionTokens int) {
	if m == nil {
		return
	}
	if promptTokens > 0 {
		m.tokens.WithLabelValues(DirectionIn).Add(float64(promptTokens))
	}
	if completionTokens > 0 {
		m.tokens.WithLabelValues(DirectionOut).Add(float64(completionTokens))
	}
}

// ToolCall records one tool dispatch. The caller is responsible for bounding
// `tool`; see MeteredExecutor.
func (m *Metrics) ToolCall(tool, outcome string) {
	if m == nil {
		return
	}
	if tool == "" {
		tool = ToolUnregistered
	}
	m.toolCalls.WithLabelValues(tool, normalise(outcome, acceptedToolOutcomes, ToolError)).Inc()
}

// SandboxFailure records a sandboxed command that never produced a verdict.
func (m *Metrics) SandboxFailure(kind string) {
	if m == nil {
		return
	}
	m.sandboxFailures.WithLabelValues(normalise(kind, acceptedSandboxKinds, SandboxExec)).Inc()
}

// ObserveRunDuration records the wall clock of one terminal ticket run.
func (m *Metrics) ObserveRunDuration(outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.runDuration.WithLabelValues(normalise(outcome, acceptedRunOutcomes, RunEscalated)).Observe(d.Seconds())
}

// normalise keeps a label inside its closed domain. An out-of-contract value
// is folded onto the most conservative in-contract one; the alternative is an
// unbounded label, which is the failure mode this whole file is arranged to
// prevent.
func normalise(v string, accepted map[string]struct{}, fallback string) string {
	if _, ok := accepted[v]; ok {
		return v
	}
	return fallback
}
