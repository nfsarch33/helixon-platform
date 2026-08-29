package agentmetrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
)

func newTestMetrics(t *testing.T) (*Metrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := New(reg, "test-rev")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, reg
}

// gathered returns every exported series as "name{label=value,...}" -> value.
// Working from the real gather output rather than from the collector handles is
// deliberate: a counter that is incremented but never registered would pass any
// assertion made against the handle, and that is precisely the bug this whole
// change exists to fix.
func gathered(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := map[string]float64{}
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			key := mf.GetName() + labelKey(m)
			switch {
			case m.GetCounter() != nil:
				out[key] = m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				out[key] = m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				out[key] = float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return out
}

func labelKey(m *dto.Metric) string {
	if len(m.GetLabel()) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestNewRegistersEveryContractName(t *testing.T) {
	_, reg := newTestMetrics(t)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	present := map[string]bool{}
	for _, mf := range families {
		present[mf.GetName()] = true
	}
	for _, name := range Names() {
		if !present[name] {
			t.Errorf("metric %q is registered nowhere; the contract lists it", name)
		}
	}
	if len(Names()) != 10 {
		t.Errorf("Names() returned %d entries, want the 10 in the frozen contract", len(Names()))
	}
}

// TestZeroSeriesExistBeforeAnyEvent is the "absence is not zero" control.
//
// If it fails, an agent that has never escalated exports no escalation series
// at all, which reads identically to an agent that is not running — and the
// difference between those two is the entire point of the alert set.
func TestZeroSeriesExistBeforeAnyEvent(t *testing.T) {
	_, reg := newTestMetrics(t)
	got := gathered(t, reg)
	want := []string{
		NameEscalations + "{reason=" + ReasonVerifierFailed + "}",
		NameEscalations + "{reason=" + ReasonBudgetExhausted + "}",
		NameEscalations + "{reason=" + ReasonRunError + "}",
		NameVerifierRuns + "{outcome=" + VerifierPass + "}",
		NameVerifierRuns + "{outcome=" + VerifierFail + "}",
		NameVerifierRuns + "{outcome=" + VerifierError + "}",
		NameSandboxFailures + "{kind=" + SandboxPreflight + "}",
		NameSandboxFailures + "{kind=" + SandboxTimeout + "}",
		NameSandboxFailures + "{kind=" + SandboxExec + "}",
		NameTokens + "{direction=" + DirectionIn + "}",
		NameTokens + "{direction=" + DirectionOut + "}",
		NameRunDuration + "{outcome=" + RunCompleted + "}",
		NameRunDuration + "{outcome=" + RunEscalated + "}",
		NameTicketsClaimed,
		NameTicketsCompleted,
		NameLoopIterations,
	}
	for _, key := range want {
		v, ok := got[key]
		if !ok {
			t.Errorf("series %q absent before any event; absence and zero must not be the same signal", key)
			continue
		}
		if v != 0 {
			t.Errorf("series %q = %v before any event, want 0", key, v)
		}
	}
}

func TestBuildInfoCarriesTheRevision(t *testing.T) {
	_, reg := newTestMetrics(t)
	got := gathered(t, reg)
	if v := got[NameBuildInfo+"{revision=test-rev}"]; v != 1 {
		t.Fatalf("%s{revision=test-rev} = %v, want 1 (got series: %v)", NameBuildInfo, v, keysOf(got))
	}
}

// TestBuildInfoIsNeverUnlabelled: an empty revision must still produce exactly
// one series, because absent build_info is the alert condition for "the agent
// is gone" and a build that forgot to stamp itself is not gone.
func TestBuildInfoIsNeverUnlabelled(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := New(reg, ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	got := gathered(t, reg)
	if v := got[NameBuildInfo+"{revision=unknown}"]; v != 1 {
		t.Fatalf("%s with an empty revision = %v, want a single series labelled unknown", NameBuildInfo, v)
	}
}

func keysOf(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestNewRejectsANilRegisterer(t *testing.T) {
	if _, err := New(nil, "x"); err == nil {
		t.Fatal("New(nil) returned no error; metrics that register nowhere are the bug being fixed")
	}
}

func TestNewRejectsDuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := New(reg, "a"); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := New(reg, "b"); err == nil {
		t.Fatal("second New on the same registry succeeded; a duplicate wiring must be an error, not a silent second copy")
	}
}

// TestLabelDomainsAreClosed is the cardinality control. Every one of these
// calls passes a value that is not in the contract; none of them may create a
// series named after it.
func TestLabelDomainsAreClosed(t *testing.T) {
	m, reg := newTestMetrics(t)
	m.Escalated("ticket-T-4821-blew-up")
	m.VerifierRun("exploded")
	m.SandboxFailure("/home/agent/work/secret.txt")
	m.ObserveRunDuration("abandoned", time.Second)
	m.ToolCall("shell", "weird")

	got := gathered(t, reg)
	for key := range got {
		for _, forbidden := range []string{"T-4821", "exploded", "secret.txt", "abandoned", "weird"} {
			if strings.Contains(key, forbidden) {
				t.Errorf("series %q leaked an out-of-contract label value %q", key, forbidden)
			}
		}
	}
	if got[NameEscalations+"{reason="+ReasonRunError+"}"] != 1 {
		t.Errorf("an unknown escalation reason did not fold onto %s", ReasonRunError)
	}
	if got[NameVerifierRuns+"{outcome="+VerifierError+"}"] != 1 {
		t.Errorf("an unknown verifier outcome did not fold onto %s", VerifierError)
	}
	if got[NameSandboxFailures+"{kind="+SandboxExec+"}"] != 1 {
		t.Errorf("an unknown sandbox kind did not fold onto %s", SandboxExec)
	}
	if got[NameToolCallsKeyOK] != 0 {
		t.Errorf("an unknown tool outcome was recorded as ok")
	}
	if got[NameToolCalls+"{outcome="+ToolError+",tool=shell}"] != 1 {
		t.Errorf("an unknown tool outcome did not fold onto %s; got %v", ToolError, keysOf(got))
	}
}

// NameToolCallsKeyOK is spelled out so the negative assertion above cannot
// silently pass by naming a series that never existed.
const NameToolCallsKeyOK = NameToolCalls + "{outcome=" + ToolOK + ",tool=shell}"

func TestCountersRecordWhatTheyAreTold(t *testing.T) {
	m, reg := newTestMetrics(t)
	m.TicketClaimed()
	m.TicketClaimed()
	m.TicketCompleted()
	m.Escalated(ReasonVerifierFailed)
	m.VerifierRun(VerifierPass)
	m.ObserveLoopIteration()
	m.ObserveLoopIteration()
	m.ObserveLoopIteration()
	m.ObserveTokens(120, 45)
	m.ToolCall("shell", ToolDenied)
	m.SandboxFailure(SandboxTimeout)
	m.ObserveRunDuration(RunCompleted, 3*time.Second)

	got := gathered(t, reg)
	for key, want := range map[string]float64{
		NameTicketsClaimed:   2,
		NameTicketsCompleted: 1,
		NameLoopIterations:   3,
		NameEscalations + "{reason=" + ReasonVerifierFailed + "}": 1,
		NameVerifierRuns + "{outcome=" + VerifierPass + "}":       1,
		NameTokens + "{direction=" + DirectionIn + "}":            120,
		NameTokens + "{direction=" + DirectionOut + "}":           45,
		NameToolCalls + "{outcome=" + ToolDenied + ",tool=shell}": 1,
		NameSandboxFailures + "{kind=" + SandboxTimeout + "}":     1,
		NameRunDuration + "{outcome=" + RunCompleted + "}":        1,
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
}

// TestNilMetricsIsInert: every call site bumps counters unconditionally, so a
// nil *Metrics must be a no-op rather than a panic. If this regresses, every
// entry point that does not wire metrics (repl, task, every library caller)
// crashes on its first tool call.
func TestNilMetricsIsInert(t *testing.T) {
	var m *Metrics
	m.TicketClaimed()
	m.TicketCompleted()
	m.Escalated(ReasonRunError)
	m.VerifierRun(VerifierPass)
	m.ObserveLoopIteration()
	m.ObserveTokens(1, 2)
	m.ToolCall("shell", ToolOK)
	m.SandboxFailure(SandboxExec)
	m.ObserveRunDuration(RunCompleted, time.Second)
}

// ---------------------------------------------------------------------------
// Cross-package contract: the producers' constants must be values this package
// accepts. Each side asserting its own copy of the strings is how two halves
// stay green while disagreeing.
// ---------------------------------------------------------------------------

func TestSandboxFailureKindsAreAcceptedLabels(t *testing.T) {
	accepted := map[string]struct{}{}
	for _, k := range AcceptedSandboxKinds() {
		accepted[k] = struct{}{}
	}
	kinds := sandbox.FailureKinds()
	if len(kinds) == 0 {
		t.Fatal("sandbox.FailureKinds() is empty; the producer side reports nothing")
	}
	for _, k := range kinds {
		if _, ok := accepted[k]; !ok {
			t.Errorf("sandbox emits failure kind %q, which this package folds away; the two sides have drifted", k)
		}
	}
	if len(kinds) != len(accepted) {
		t.Errorf("sandbox emits %d kinds but %d are accepted; one side gained a value the other never heard of", len(kinds), len(accepted))
	}
}

// ---------------------------------------------------------------------------
// MeteredExecutor
// ---------------------------------------------------------------------------

type stubExecutor struct {
	tools []string
	err   error
}

func (s *stubExecutor) Execute(_ context.Context, _, _ string) (string, error) {
	return "out", s.err
}

func (s *stubExecutor) Available() []llm.Tool {
	out := make([]llm.Tool, 0, len(s.tools))
	for _, name := range s.tools {
		out = append(out, llm.Tool{Type: "function", Function: llm.FunctionDef{Name: name}})
	}
	return out
}

func TestMeteredExecutorBoundsTheToolLabel(t *testing.T) {
	m, reg := newTestMetrics(t)
	e := NewMeteredExecutor(&stubExecutor{tools: []string{"shell", "file_read"}}, m)

	if _, err := e.Execute(context.Background(), "shell", "{}"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// A model is free to invent a tool name; this decorator is not free to
	// invent a time series for each one.
	for _, invented := range []string{"rm_rf_slash", "exfiltrate", "definitely_a_tool"} {
		if _, err := e.Execute(context.Background(), invented, "{}"); err != nil {
			t.Fatalf("Execute %s: %v", invented, err)
		}
	}

	got := gathered(t, reg)
	if got[NameToolCalls+"{outcome="+ToolOK+",tool=shell}"] != 1 {
		t.Errorf("registered tool was not counted under its own name: %v", keysOf(got))
	}
	if got[NameToolCalls+"{outcome="+ToolOK+",tool="+ToolUnregistered+"}"] != 3 {
		t.Errorf("three invented tool names did not collapse onto %q: %v", ToolUnregistered, keysOf(got))
	}
	for key := range got {
		for _, invented := range []string{"rm_rf_slash", "exfiltrate", "definitely_a_tool"} {
			if strings.Contains(key, invented) {
				t.Errorf("series %q was minted from a model-supplied tool name", key)
			}
		}
	}
}

func TestMeteredExecutorPassesThroughOutputAndError(t *testing.T) {
	m, _ := newTestMetrics(t)
	sentinel := errors.New("boom")
	e := NewMeteredExecutor(&stubExecutor{tools: []string{"shell"}, err: sentinel}, m)
	out, err := e.Execute(context.Background(), "shell", "{}")
	if out != "out" {
		t.Errorf("output = %q, want it forwarded unchanged", out)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want the inner error forwarded unchanged", err)
	}
	if len(e.Available()) != 1 {
		t.Errorf("Available() did not proxy to the inner executor")
	}
}

func TestClassifyToolErrorSeparatesDeniedFromBroken(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil is ok", nil, ToolOK},
		{"policy denial", sandbox.ErrToolDenied, ToolDenied},
		{"wrapped policy denial", errors.Join(errors.New("ctx"), sandbox.ErrToolDenied), ToolDenied},
		{"host execution refused", sandbox.ErrHostExecutionRefused, ToolDenied},
		{"loop guard", loopguard.ErrLoopDetected, ToolDenied},
		{"anything else", errors.New("the tool exploded"), ToolError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyToolError(tc.err); got != tc.want {
				t.Errorf("ClassifyToolError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
