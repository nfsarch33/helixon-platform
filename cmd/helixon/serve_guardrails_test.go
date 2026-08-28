package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

func serveTestConfig(t *testing.T) helixon.RuntimeConfig {
	t.Helper()
	cfg, err := helixon.DefaultRuntimeConfig()
	if err != nil {
		t.Fatalf("DefaultRuntimeConfig: %v", err)
	}
	cfg.AgentID = "serve-test"
	cfg.SessionDSN = filepath.Join(t.TempDir(), "serve.db")
	cfg.Sandbox.Workspace = t.TempDir()
	cfg.Agentrace.LogPath = filepath.Join(t.TempDir(), "trace.ndjson")
	return cfg
}

// TestInitAndConfigureRuntime_InstallsEveryGuardrail is the serve-path
// counterpart of the task-path E2E: `helixon serve` must come up with the
// sandbox gate, the loop guard and the trace all in the chain, not just the
// tools.
func TestInitAndConfigureRuntime_InstallsEveryGuardrail(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)

	if err := initAndConfigureRuntime(context.Background(), rt, cfg, ""); err != nil {
		t.Fatalf("initAndConfigureRuntime: %v", err)
	}
	if rt.SandboxRunner() == nil {
		t.Fatal("the sandbox gate must be installed on the serve path")
	}
	if rt.Phase() != helixon.PhaseConfigured {
		t.Fatalf("phase = %s, want configured", rt.Phase())
	}
	names := strings.Join(rt.Registry().Names(), ",")
	for _, want := range []string{"shell", "file_read", "file_write", "web_fetch", "verifier_run"} {
		if !strings.Contains(names, want) {
			t.Errorf("serve must register %q; got %s", want, names)
		}
	}
	// The gate is really in the executor chain: a traversal is refused
	// before any handler runs.
	if _, err := rt.Executor().Execute(context.Background(), "file_write", `{"path":"/etc/passwd"}`); err == nil {
		t.Fatal("the sandbox path guard is not in the serve executor chain")
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })
}

func TestBuildServeRuntime(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	cfg.Provider = helixon.ProviderConfig{Kind: "mock"}
	cfg.SprintboardURL = "http://127.0.0.1:9400"

	rt, err := buildServeRuntime(cfg)
	if err != nil {
		t.Fatalf("buildServeRuntime: %v", err)
	}
	if rt == nil {
		t.Fatal("expected a runtime")
	}

	bad := cfg
	bad.Provider = helixon.ProviderConfig{Kind: "not-a-provider"}
	if _, err := buildServeRuntime(bad); err == nil {
		t.Fatal("an unknown provider kind must fail the build")
	}
}

// TestPrintServeBanner_Content asserts what the banner actually says.
// serve_refactor_test.go already has a TestPrintServeBanner that only checks
// the function exists; this is the behavioral half.
func TestPrintServeBanner_Content(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	var buf bytes.Buffer
	printServeBanner(&buf, "agent-x", rt, "127.0.0.1:9999")
	got := buf.String()
	for _, want := range []string{`agent_id="agent-x"`, "heartbeat_every=", "HTTP channel on 127.0.0.1:9999"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q; got %q", want, got)
		}
	}
	var quiet bytes.Buffer
	printServeBanner(&quiet, "agent-x", rt, "")
	if strings.Contains(quiet.String(), "HTTP channel") {
		t.Errorf("no HTTP line expected without an address; got %q", quiet.String())
	}
}

func TestRuntimeView_Accessors(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	v := runtimeView{rt: rt}
	if v.AgentID() != "serve-test" {
		t.Errorf("AgentID = %q", v.AgentID())
	}
	if v.Phase() == "" {
		t.Error("Phase must be non-empty")
	}
	if v.HeartbeatEvery() <= 0 {
		t.Error("HeartbeatEvery must be positive")
	}
	if v.ChannelCount() != 0 {
		t.Errorf("ChannelCount = %d", v.ChannelCount())
	}
	if v.RegisteredToolCount() != 0 {
		t.Errorf("RegisteredToolCount = %d", v.RegisteredToolCount())
	}
}

// scriptedCLIProvider drives the agent loop from a cmd-level test.
type scriptedCLIProvider struct {
	responses []*llm.CompletionResponse
	idx       int
}

func (p *scriptedCLIProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) { //nolint:gocritic // hugeParam: llm.Provider's signature
	if p.idx >= len(p.responses) {
		return nil, fmt.Errorf("scripted provider exhausted after %d calls", p.idx)
	}
	r := p.responses[p.idx]
	p.idx++
	return r, nil
}

// TestExecuteAndReport_EscalationDoesNotCompleteTheTicket is the end of the
// escalation story at the CLI boundary. Reporting the ticket complete with
// "stopped for human approval" as the evidence would be the exact failure the
// gate exists to prevent, so the ticket must be left claimed.
func TestExecuteAndReport_EscalationDoesNotCompleteTheTicket(t *testing.T) {
	t.Parallel()

	var completed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "complete") {
			completed.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	workspace := t.TempDir()
	target := filepath.Join(workspace, "changed.txt")
	writeCall := llm.ToolCall{
		ID: "w1", Type: "function",
		Function: llm.FunctionCall{
			Name:      "file_write",
			Arguments: fmt.Sprintf(`{"path":%q,"content":"edited"}`, target),
		},
	}
	provider := &scriptedCLIProvider{responses: []*llm.CompletionResponse{
		{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{writeCall}}}}},
		{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done, honest"}}}},
	}}

	cfg := serveTestConfig(t)
	cfg.Sandbox.Workspace = workspace
	rt := helixon.NewRuntime(provider, cfg)
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	g, err := newGuardrails(cfg)
	if err != nil {
		t.Fatalf("newGuardrails: %v", err)
	}
	if err := builtins.RegisterAll(rt.Registry(), g.toolOptions(false)); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if err := rt.Configure(context.Background(), g.configOptions()...); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	sb := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "serve-test",
	}, nil)

	var out bytes.Buffer
	_, runErr := executeAndReport(context.Background(), rt, "edit the file", "TICKET-1", sb, &out)
	if runErr == nil {
		t.Fatal("a state-changing run with no verifier evidence must not succeed")
	}
	got := out.String()
	if !strings.Contains(got, "STOPPED FOR HUMAN APPROVAL") {
		t.Errorf("expected the escalation banner; got %q", got)
	}
	if !strings.Contains(got, "NOT completed") {
		t.Errorf("expected the ticket to be reported as not completed; got %q", got)
	}
	if n := completed.Load(); n != 0 {
		t.Errorf("the ticket was completed %d time(s); an escalation is not a completion", n)
	}
	// The write itself did happen — the gate is about REPORTING, not about
	// pretending the run did nothing.
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("expected the file_write tool call to have run: %v", statErr)
	}
}

func TestSandboxPolicyFor_DenyUnlisted(t *testing.T) {
	t.Parallel()
	base := sandbox.PolicyFor(sandbox.Config{})
	if got := base.For("some_new_tool"); got != sandbox.DispositionPathGuard {
		t.Errorf("default policy for an unlisted tool = %s, want path_guard", got)
	}
	strict := sandbox.PolicyFor(sandbox.Config{DenyUnlistedTools: true})
	if got := strict.For("some_new_tool"); got != sandbox.DispositionDeny {
		t.Errorf("deny_unlisted_tools policy for an unlisted tool = %s, want deny", got)
	}
	if got := strict.For("shell"); got != sandbox.DispositionSandbox {
		t.Errorf("deny_unlisted_tools must not change a classified tool; shell = %s", got)
	}
}
