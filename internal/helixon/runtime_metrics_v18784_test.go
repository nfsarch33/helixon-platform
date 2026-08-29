package helixon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"

	_ "modernc.org/sqlite"
)

// These tests assert the RUNTIME wiring rather than the collectors. Each one
// exists because its counter is bumped in a place a refactor can quietly
// detach: an option dropped from a list, an interface field left nil, a
// decorator applied in the wrong order. The collectors' own tests would stay
// green through every one of those.

func metricsRuntime(t *testing.T, provider llm.Provider, extra ...ConfigOption) (*Runtime, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := agentmetrics.New(reg, "runtime-test")
	if err != nil {
		t.Fatalf("agentmetrics.New: %v", err)
	}
	cfg := RuntimeConfig{
		AgentID:    "metrics-agent",
		SessionDSN: "file:" + filepath.Join(t.TempDir(), "m.db") + "?cache=shared&mode=rwc",
		Timeout:    20 * time.Second,
		Logger:     quietLogger(),
	}
	rt := NewRuntime(provider, cfg)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := rt.Registry().Register(tooldispatch.ToolDef{
		Name:        "echo_tool",
		Description: "echo",
		Handler:     func(_ context.Context, _ map[string]any) (string, error) { return "ok", nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Metrics LAST, exactly as guardrails.configOptions orders them.
	opts := append(append([]ConfigOption{}, extra...), WithAgentMetrics(m))
	if err := rt.Configure(ctx, opts...); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })
	return rt, reg
}

// TestRuntimeCountsLoopIterationsAndTokens: the loop observer is an interface
// field on agent.Config. Leaving it nil costs nothing at compile time and
// silences two counters.
func TestRuntimeCountsLoopIterationsAndTokens(t *testing.T) {
	rt, reg := metricsRuntime(t, &recordingProvider{reply: "answered"})

	if _, err := rt.HandleMessage(context.Background(), IncomingMessage{Channel: "test", Content: "hello"}); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	got := seriesOf(t, reg)
	if got[agentmetrics.NameLoopIterations] != 1 {
		t.Errorf("%s = %v, want 1", agentmetrics.NameLoopIterations, got[agentmetrics.NameLoopIterations])
	}
	if got[agentmetrics.NameTokens+"{direction="+agentmetrics.DirectionIn+"}"] != 11 {
		t.Errorf("tokens in = %v, want the 11 the provider reported", got[agentmetrics.NameTokens+"{direction=in}"])
	}
	if got[agentmetrics.NameTokens+"{direction="+agentmetrics.DirectionOut+"}"] != 7 {
		t.Errorf("tokens out = %v, want the 7 the provider reported", got[agentmetrics.NameTokens+"{direction=out}"])
	}
}

// TestRuntimeWithoutMetricsCountsNothing is the positive control: same runtime,
// same message, no WithAgentMetrics.
func TestRuntimeWithoutMetricsCountsNothing(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := agentmetrics.New(reg, "control"); err != nil {
		t.Fatalf("agentmetrics.New: %v", err)
	}
	cfg := RuntimeConfig{
		AgentID:    "bare-agent",
		SessionDSN: "file:" + filepath.Join(t.TempDir(), "bare.db") + "?cache=shared&mode=rwc",
		Timeout:    20 * time.Second,
		Logger:     quietLogger(),
	}
	rt := NewRuntime(&recordingProvider{reply: "answered"}, cfg)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := rt.Configure(ctx); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	if _, err := rt.HandleMessage(ctx, IncomingMessage{Channel: "test", Content: "hello"}); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := seriesOf(t, reg)
	if got[agentmetrics.NameLoopIterations] != 0 {
		t.Errorf("%s moved with no metrics wired", agentmetrics.NameLoopIterations)
	}
	if got[agentmetrics.NameTokens+"{direction="+agentmetrics.DirectionIn+"}"] != 0 {
		t.Error("token counter moved with no metrics wired")
	}
}

// TestRuntimeCountsToolCalls: the metered decorator has to be IN the executor
// chain, not merely constructed.
func TestRuntimeCountsToolCalls(t *testing.T) {
	rt, reg := metricsRuntime(t, &recordingProvider{reply: "x"})

	if _, err := rt.Executor().Execute(context.Background(), "echo_tool", "{}"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := rt.Executor().Execute(context.Background(), "no_such_tool", "{}"); err == nil {
		t.Fatal("an unregistered tool must fail")
	}

	got := seriesOf(t, reg)
	okKey := agentmetrics.NameToolCalls + "{outcome=" + agentmetrics.ToolOK + ",tool=echo_tool}"
	if got[okKey] != 1 {
		t.Errorf("%s = %v, want 1", okKey, got[okKey])
	}
	errKey := agentmetrics.NameToolCalls + "{outcome=" + agentmetrics.ToolError + ",tool=" + agentmetrics.ToolUnregistered + "}"
	if got[errKey] != 1 {
		t.Errorf("%s = %v, want 1", errKey, got[errKey])
	}
}

// TestMeteredExecutorSitsOutsideTheLoopGuard: a refusal never reaches the
// registry, so a counter installed under the guardrails would report a quiet,
// healthy agent for one being denied at every turn.
func TestMeteredExecutorSitsOutsideTheLoopGuard(t *testing.T) {
	rt, reg := metricsRuntime(t, &recordingProvider{reply: "x"},
		WithLoopGuard(LoopGuardConfig{Enabled: true, Threshold: 2, Window: time.Minute}))

	var lastErr error
	for i := 0; i < 6; i++ {
		_, lastErr = rt.Executor().Execute(context.Background(), "echo_tool", `{"same":"args"}`)
	}
	if lastErr == nil {
		t.Fatal("the loop guard never tripped; the scenario under test did not happen")
	}
	if !errors.Is(lastErr, loopguard.ErrLoopDetected) {
		t.Fatalf("last error = %v, want a loop-guard refusal", lastErr)
	}

	got := seriesOf(t, reg)
	deniedKey := agentmetrics.NameToolCalls + "{outcome=" + agentmetrics.ToolDenied + ",tool=echo_tool}"
	if got[deniedKey] == 0 {
		t.Errorf("%s = 0; refused calls are invisible, which is the case an operator most needs to see", deniedKey)
	}
}

// TestTicketPollerReceivesTheRuntimeMetrics: buildTicketPoller passes the
// metrics through an option that is easy to drop, and the poller owns the
// escalation counter — the load-bearing one.
func TestTicketPollerReceivesTheRuntimeMetrics(t *testing.T) {
	board := newFakeBoard()
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "metrics-agent",
	}, quietLogger())

	reg := prometheus.NewRegistry()
	m, err := agentmetrics.New(reg, "poller-wiring")
	if err != nil {
		t.Fatalf("agentmetrics.New: %v", err)
	}
	cfg := RuntimeConfig{
		AgentID:    "metrics-agent",
		SessionDSN: "file:" + filepath.Join(t.TempDir(), "p.db") + "?cache=shared&mode=rwc",
		Timeout:    time.Second,
		Logger:     quietLogger(),
		Tickets: TicketPollerConfig{
			Enabled: true, Interval: time.Millisecond, MaxConcurrent: 1, TicketTimeout: 30 * time.Second,
		},
	}
	rt := NewRuntime(&recordingProvider{reply: "x"}, cfg)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := rt.Configure(ctx, WithSprintboard(client), WithAgentMetrics(m)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	p := rt.TicketPoller()
	if p == nil {
		t.Fatal("ticket polling is enabled but no poller was built")
	}
	if p.metrics == nil {
		t.Fatal("the poller has no metrics; every escalation would go uncounted and no alert could fire")
	}
}
