package helixon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

//nolint:gocritic // hugeParam: test helper, copying the config is the point
func guardrailRuntime(t *testing.T, cfg helixon.RuntimeConfig) *helixon.Runtime {
	t.Helper()
	if cfg.SessionDSN == "" {
		cfg.SessionDSN = filepath.Join(t.TempDir(), "guardrails.db")
	}
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return rt
}

// echoTool is a harmless tool used to observe the decorator chain.
func echoTool() tooldispatch.ToolDef {
	return tooldispatch.ToolDef{
		Name:        "echo_tool",
		Description: "echo",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"v":{"type":"string"}}}`),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			s, _ := args["v"].(string)
			return s, nil
		},
	}
}

// TestWithSandbox_DisabledWithoutTheExplicitOptOutIsAnError is the
// anti-silent-degradation assertion at the wiring layer: a config that turns
// the boundary off without saying so refuses to start.
func TestWithSandbox_DisabledWithoutTheExplicitOptOutIsAnError(t *testing.T) {
	t.Parallel()
	rt := guardrailRuntime(t, helixon.RuntimeConfig{})
	err := rt.Configure(context.Background(), helixon.WithSandbox(sandbox.Config{Enabled: false}))
	if err == nil {
		t.Fatal("a disabled sandbox with no explicit opt-out must be refused")
	}
	if !strings.Contains(err.Error(), "allow_unsandboxed_host_execution") {
		t.Fatalf("the error must name the opt-out key; got %q", err)
	}
}

// TestWithSandbox_ExplicitHostExecutionIsPermittedButUngated proves the
// escape hatch works and that it really does leave no gate behind.
func TestWithSandbox_ExplicitHostExecutionIsPermitted(t *testing.T) {
	t.Parallel()
	rt := guardrailRuntime(t, helixon.RuntimeConfig{})
	err := rt.Configure(context.Background(), helixon.WithSandbox(sandbox.Config{
		Enabled:                       false,
		AllowUnsandboxedHostExecution: true,
	}))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if rt.SandboxRunner() != nil {
		t.Fatal("no runner should be installed when host execution is chosen")
	}
}

func TestWithSandbox_InstallsTheGate(t *testing.T) {
	t.Parallel()
	rt := guardrailRuntime(t, helixon.RuntimeConfig{})
	if err := rt.Registry().Register(echoTool()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := rt.Configure(context.Background(), helixon.WithSandbox(sandbox.Config{
		Enabled:   true,
		Workspace: t.TempDir(),
	}))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	runner := rt.SandboxRunner()
	if runner == nil {
		t.Fatal("SandboxRunner must be exposed once the gate is installed")
	}
	if runner.Config().Network != "none" {
		t.Fatalf("network = %q, want none", runner.Config().Network)
	}
}

func TestWithSandbox_InvalidConfigFailsConfigure(t *testing.T) {
	t.Parallel()
	rt := guardrailRuntime(t, helixon.RuntimeConfig{})
	err := rt.Configure(context.Background(), helixon.WithSandbox(sandbox.Config{
		Enabled: true, Engine: "docker", Workspace: t.TempDir(),
	}))
	if err == nil || !strings.Contains(err.Error(), "podman is the only permitted engine") {
		t.Fatalf("Configure = %v, want the engine refusal", err)
	}
}

func TestWithSandbox_RequiresInit(t *testing.T) {
	t.Parallel()
	rt := helixon.NewRuntime(nil, helixon.RuntimeConfig{})
	// Configure before Init: the phase check fires first, but the option
	// itself must also refuse a nil executor.
	if err := helixon.WithSandbox(sandbox.Config{Enabled: true, Workspace: t.TempDir()})(rt); err == nil {
		t.Fatal("WithSandbox must require Init")
	}
	if err := helixon.WithLoopGuard(helixon.LoopGuardConfig{Enabled: true})(rt); err == nil {
		t.Fatal("WithLoopGuard must require Init")
	}
}

// TestWithLoopGuard_TripsOnRepeatedIdenticalCalls proves the LoopGuard is
// actually in the chain now. Before v18779 it was implemented, tested, and
// wired into nothing.
func TestWithLoopGuard_TripsOnRepeatedIdenticalCalls(t *testing.T) {
	t.Parallel()
	rt := guardrailRuntime(t, helixon.RuntimeConfig{})
	if err := rt.Registry().Register(echoTool()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := rt.Configure(context.Background(),
		helixon.WithSandbox(sandbox.Config{Enabled: false, AllowUnsandboxedHostExecution: true}),
		helixon.WithLoopGuard(helixon.LoopGuardConfig{Enabled: true, Threshold: 3, Window: time.Minute}),
	)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	exec := rt.Executor()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := exec.Execute(ctx, "echo_tool", `{"v":"same"}`); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := exec.Execute(ctx, "echo_tool", `{"v":"same"}`); err == nil {
		t.Fatal("the 3rd identical call must trip the loop guard")
	}
	// A DIFFERENT call is unaffected.
	if _, err := exec.Execute(ctx, "echo_tool", `{"v":"other"}`); err != nil {
		t.Fatalf("a distinct call must still be dispatched: %v", err)
	}
}

func TestWithLoopGuard_DisabledIsANoOp(t *testing.T) {
	t.Parallel()
	rt := guardrailRuntime(t, helixon.RuntimeConfig{})
	if err := rt.Registry().Register(echoTool()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := rt.Configure(context.Background(),
		helixon.WithSandbox(sandbox.Config{Enabled: false, AllowUnsandboxedHostExecution: true}),
		helixon.WithLoopGuard(helixon.LoopGuardConfig{Enabled: false, Threshold: 1}),
	)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := rt.Executor().Execute(context.Background(), "echo_tool", `{"v":"x"}`); err != nil {
			t.Fatalf("call %d must succeed with the guard disabled: %v", i, err)
		}
	}
}

// TestDecoratorChain_AgentraceRecordsSandboxRefusals proves the ordering:
// agentrace wraps the sandbox gate, so a refusal is traced rather than
// vanishing.
func TestDecoratorChain_AgentraceRecordsSandboxRefusals(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "agentrace.ndjson")
	rt := guardrailRuntime(t, helixon.RuntimeConfig{AgentID: "trace-test"})
	if err := rt.Registry().Register(echoTool()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := rt.Configure(context.Background(),
		helixon.WithSandbox(sandbox.Config{Enabled: true, Workspace: t.TempDir()}),
		helixon.WithAgentrace(tooldispatch.AgentraceConfig{LogPath: logPath, AgentID: "trace-test"}),
	)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	// file_write with a traversal path: refused by the sandbox path guard.
	_, execErr := rt.Executor().Execute(context.Background(), "file_write", `{"path":"/etc/passwd"}`)
	if execErr == nil {
		t.Fatal("the path guard must refuse /etc/passwd")
	}
	if err := rt.Shutdown(context.Background()); err == nil {
		t.Log("shutdown returned nil (runtime was never Run); the sink is flushed either way")
	}

	data, readErr := os.ReadFile(logPath) //nolint:gosec // G304 test-controlled path
	if readErr != nil {
		t.Fatalf("read agentrace log: %v", readErr)
	}
	if !strings.Contains(string(data), `"tool":"file_write"`) {
		t.Fatalf("the refusal was not traced; log = %q", data)
	}
	if !strings.Contains(string(data), `"success":false`) {
		t.Fatalf("the trace must record the refusal as unsuccessful; log = %q", data)
	}
}
