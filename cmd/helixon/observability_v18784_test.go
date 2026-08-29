package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

func fetch(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func echo(_ context.Context, msg helixon.IncomingMessage) (string, error) {
	return "echo:" + msg.Content, nil
}

// TestServeWiresMetricsIntoTheRuntime: the wiring that was missing is exactly
// the kind an inline struct literal hides, so it is asserted at the seam the
// serve path actually goes through.
func TestServeWiresMetricsIntoTheRuntime(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)

	if err := initAndConfigureRuntime(context.Background(), rt, cfg, ""); err != nil {
		t.Fatalf("initAndConfigureRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	if rt.Metrics() == nil {
		t.Fatal("serve came up with no agent metrics; :8686/metrics would 404 again")
	}
}

// TestServeWithMetricsDisabledWiresNothing is the positive control for the test
// above: flip the config and the runtime must come up bare, so a green metrics
// assertion cannot be satisfied by something other than the flag.
func TestServeWithMetricsDisabledWiresNothing(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	cfg.Metrics.Enabled = false
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)

	if err := initAndConfigureRuntime(context.Background(), rt, cfg, ""); err != nil {
		t.Fatalf("initAndConfigureRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	if rt.Metrics() != nil {
		t.Fatal("metrics were wired despite metrics.enabled being false")
	}
}

// TestServeChannelServesTheContract drives the channel the serve path builds,
// with the gatherer the serve path produces.
func TestServeChannelServesTheContract(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	m, gatherer, err := newAgentObservability(cfg, "deadbeef")
	if err != nil {
		t.Fatalf("newAgentObservability: %v", err)
	}
	if m == nil || gatherer == nil {
		t.Fatal("metrics are on by default but newAgentObservability produced nothing")
	}
	ch := newServeHTTPChannel("127.0.0.1:0", gatherer)
	srv := httptest.NewServer(ch.Routes(echo))
	t.Cleanup(srv.Close)

	if code, _ := fetch(t, srv.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", code)
	}
	code, body := fetch(t, srv.URL+"/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", code)
	}
	for _, name := range agentmetrics.Names() {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics does not expose %q", name)
		}
	}
	if !strings.Contains(body, `revision="deadbeef"`) {
		t.Error("/metrics does not identify the running build")
	}
}

// TestServeChannelWithoutMetricsServesNo404Substitute is the positive control
// for the exposition wiring. Revert newAgentObservability to return nothing and
// /metrics must 404 rather than fall back to the process-global registry —
// which would answer 200 with Go runtime series from a binary running no agent.
func TestServeChannelWithoutMetricsServesNo404Substitute(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	cfg.Metrics.Enabled = false
	m, gatherer, err := newAgentObservability(cfg, "deadbeef")
	if err != nil {
		t.Fatalf("newAgentObservability: %v", err)
	}
	if m != nil || gatherer != nil {
		t.Fatal("metrics.enabled=false still produced a registry")
	}
	ch := newServeHTTPChannel("127.0.0.1:0", gatherer)
	srv := httptest.NewServer(ch.Routes(echo))
	t.Cleanup(srv.Close)

	if code, _ := fetch(t, srv.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200 even with metrics off", code)
	}
	code, body := fetch(t, srv.URL+"/metrics")
	if code == http.StatusOK {
		t.Fatalf("GET /metrics = 200 with no registry wired (body starts %q)", body[:min(len(body), 120)])
	}
	if code != http.StatusNotFound {
		t.Errorf("GET /metrics = %d, want 404", code)
	}
}

// TestGuardrailsWireTheSandboxAndVerifierObservers: the sandbox and verifier
// halves report through callbacks that are easy to forget, so the wiring is
// asserted rather than assumed.
func TestGuardrailsWireTheSandboxAndVerifierObservers(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	m, gatherer, err := newAgentObservability(cfg, "rev")
	if err != nil {
		t.Fatalf("newAgentObservability: %v", err)
	}
	g, err := newGuardrails(cfg)
	if err != nil {
		t.Fatalf("newGuardrails: %v", err)
	}
	g = g.withMetrics(m)

	opts := g.toolOptions(true)
	if opts.Verifier == nil {
		t.Fatal("the verifier tool is not configured on the serve path")
	}
	if opts.Verifier.OnOutcome == nil {
		t.Error("verifier_run reports no outcome; the verifier counter would stay at zero forever")
	}

	// The sandbox observer is a callback on the shared runner, so it is
	// asserted by driving a refused run through THAT runner and reading the
	// series back off the exposition — which proves the callback reached the
	// runner the tools use, not a copy of it.
	if g.runner == nil {
		t.Fatal("no sandbox runner on the serve path")
	}
	if _, err := g.runner.Run(context.Background(), sandbox.Spec{
		Command: "/bin/sh", Args: []string{"-c", "id"},
	}); err == nil {
		t.Fatal("expected the sandbox to refuse a path-shaped command")
	}

	ch := newServeHTTPChannel("127.0.0.1:0", gatherer)
	srv := httptest.NewServer(ch.Routes(echo))
	t.Cleanup(srv.Close)
	_, body := fetch(t, srv.URL+"/metrics")
	const want = `hlxn_agent_sandbox_failures_total{kind="preflight"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("exposition does not carry %q; the sandbox failure observer is not attached", want)
	}
}

// TestGuardrailsWithoutMetricsLeaveObserversUnset is the control: without the
// withMetrics step nothing may be attached, so the assertions above cannot pass
// by accident.
func TestGuardrailsWithoutMetricsLeaveObserversUnset(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	g, err := newGuardrails(cfg)
	if err != nil {
		t.Fatalf("newGuardrails: %v", err)
	}
	opts := g.toolOptions(true)
	if opts.Verifier == nil {
		t.Fatal("the verifier tool is not configured")
	}
	if opts.Verifier.OnOutcome != nil {
		t.Error("an outcome observer was attached with no metrics wired")
	}
}

// TestResolveMemoryURLPrefersTheConfigFile: an explicit YAML value always wins
// over the ambient environment, or a host variable could silently redirect
// where an operator said memory should go.
func TestResolveMemoryURLPrefersTheConfigFile(t *testing.T) {
	t.Parallel()
	got := resolveMemoryURL(helixon.LoopMemoryConfig{EngramURL: "http://from-yaml"}, "http://from-env")
	if got.EngramURL != "http://from-yaml" {
		t.Errorf("EngramURL = %q, want the YAML value", got.EngramURL)
	}
	got = resolveMemoryURL(helixon.LoopMemoryConfig{}, "  http://from-env  ")
	if got.EngramURL != "http://from-env" {
		t.Errorf("EngramURL = %q, want the trimmed env value", got.EngramURL)
	}
	got = resolveMemoryURL(helixon.LoopMemoryConfig{}, "")
	if got.EngramURL != "" {
		t.Errorf("EngramURL = %q, want empty when neither source names a server", got.EngramURL)
	}
}

// TestBuildAgentMemoryNeedsAServer: memory is default-ON, so URL-presence is
// what stops an upgraded binary reaching for a destination nobody configured.
func TestBuildAgentMemoryNeedsAServer(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	if buildAgentMemory(cfg) != nil {
		t.Error("memory was built with no engram_url configured")
	}
	cfg.Memory.EngramURL = "http://127.0.0.1:1"
	if buildAgentMemory(cfg) == nil {
		t.Error("memory was not built despite a configured server")
	}
	cfg.Memory.Enabled = false
	if buildAgentMemory(cfg) != nil {
		t.Error("memory.enabled=false did not switch memory off")
	}
}

// TestServeWiresMemoryWhenConfigured closes the loop: the serve path itself,
// not just the helper, must hand the runtime its memory.
func TestServeWiresMemoryWhenConfigured(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	cfg.Memory.EngramURL = "http://127.0.0.1:1"
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)
	if err := initAndConfigureRuntime(context.Background(), rt, cfg, ""); err != nil {
		t.Fatalf("initAndConfigureRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })
	if rt.AgentMemory() == nil {
		t.Fatal("serve did not wire loop memory despite a configured engram_url")
	}

	bare := serveTestConfig(t)
	rt2 := helixon.NewRuntime(llm.NewMockProvider(), bare)
	if err := initAndConfigureRuntime(context.Background(), rt2, bare, ""); err != nil {
		t.Fatalf("initAndConfigureRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt2.Shutdown(context.Background()) })
	if rt2.AgentMemory() != nil {
		t.Fatal("serve wired loop memory with no engram_url; the control fails")
	}
}
