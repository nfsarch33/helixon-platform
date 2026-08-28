package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"

	_ "modernc.org/sqlite"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helixon.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestE2E_TaskPipelineStillWorksWithTheGuardrails is the regression guard for
// the live path: `helixon task` must still claim, run, and complete a ticket
// once the sandbox gate, loop guard, agentrace recorder and completion gate
// are all in the chain.
//
// The mock provider answers with text and no tool calls, so no container is
// started and the test does not need podman.
func TestE2E_TaskPipelineStillWorksWithTheGuardrails(t *testing.T) {
	// No t.Parallel: t.Setenv pins XDG_STATE_HOME for the agentrace path.
	workspace := t.TempDir()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	cfgPath := writeConfig(t, ""+
		"agent_id: task-e2e\n"+
		"session_dsn: \"file:"+filepath.Join(t.TempDir(), "task.db")+"?cache=shared&mode=rwc\"\n"+
		"provider:\n  kind: mock\n"+
		"sandbox:\n  workspace: "+workspace+"\n")

	var out bytes.Buffer
	err := runTaskPipeline(context.Background(), taskArgs{
		configPath: cfgPath,
		prompt:     "summarize the current state",
	}, taskDeps{}, &out)
	if err != nil {
		t.Fatalf("runTaskPipeline: %v (output=%q)", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "--- Result ---") {
		t.Fatalf("expected a result section; got %q", got)
	}
	// verifier_run is registered alongside the three original builtins.
	if !strings.Contains(got, "tools=4") {
		t.Fatalf("expected 4 registered tools (shell, file_read, file_write, verifier_run); got %q", got)
	}
	// The agentrace recorder is wired and writing where the resolver says.
	traceDir := filepath.Join(state, "helixon")
	entries, readErr := os.ReadDir(traceDir)
	if readErr != nil {
		t.Fatalf("agentrace directory %s was not created: %v", traceDir, readErr)
	}
	if len(entries) == 0 {
		t.Fatalf("no agentrace file was created in %s", traceDir)
	}
}

// TestSetupTaskRuntime_RefusesASilentlyDisabledSandbox proves the CLI carries
// the same refusal the runtime does: a config that turns the boundary off
// without saying so does not start.
func TestSetupTaskRuntime_RefusesASilentlyDisabledSandbox(t *testing.T) {
	t.Parallel()
	cfgPath := writeConfig(t, "agent_id: x\nprovider:\n  kind: mock\nsandbox:\n  enabled: false\n")
	_, _, err := setupTaskRuntime(context.Background(), cfgPath)
	if err == nil || !strings.Contains(err.Error(), "allow_unsandboxed_host_execution") {
		t.Fatalf("setupTaskRuntime = %v, want the explicit-opt-out refusal", err)
	}
}

func TestSetupTaskRuntime_AcceptsTheExplicitOptOut(t *testing.T) {
	// No t.Parallel: t.Setenv.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfgPath := writeConfig(t, ""+
		"agent_id: optout\nprovider:\n  kind: mock\n"+
		"sandbox:\n  enabled: false\n  allow_unsandboxed_host_execution: true\n")
	rt, _, err := setupTaskRuntime(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("setupTaskRuntime: %v", err)
	}
	if rt.SandboxRunner() != nil {
		t.Fatal("no sandbox runner should exist on the explicit host-execution path")
	}
	// Without a runner there is no verifier: a host-side verifier would be a
	// second unsandboxed execution primitive, not a proof of work.
	for _, name := range rt.Registry().Names() {
		if name == "verifier_run" {
			t.Fatal("verifier_run must not be registered without a sandbox")
		}
	}
}

func TestGuardrails_ToolOptionsPinFilePathsToTheWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	cfg, err := helixon.DefaultRuntimeConfig()
	if err != nil {
		t.Fatalf("DefaultRuntimeConfig: %v", err)
	}
	cfg.Sandbox.Workspace = workspace

	g, err := newGuardrails(cfg)
	if err != nil {
		t.Fatalf("newGuardrails: %v", err)
	}
	opts := g.toolOptions(true)
	if len(opts.FileRead.AllowedPaths) != 1 || !strings.HasSuffix(opts.FileRead.AllowedPaths[0], filepath.Base(workspace)) {
		t.Fatalf("file_read must be pinned to the workspace; got %v", opts.FileRead.AllowedPaths)
	}
	if len(opts.FileWrite.AllowedPaths) != 1 {
		t.Fatalf("file_write must be pinned to the workspace; got %v", opts.FileWrite.AllowedPaths)
	}
	if opts.Verifier == nil || opts.Verifier.Runner == nil {
		t.Fatal("the verifier must be registered when a sandbox exists")
	}
	if opts.WebFetch == nil {
		t.Fatal("web_fetch was requested and should be present")
	}
}

func TestAgentraceLogPath(t *testing.T) {
	// No t.Parallel: the subtests call t.Setenv.
	tests := []struct {
		name     string
		cfg      helixon.RuntimeConfig
		stateEnv string
		want     string
	}{
		{
			name: "explicit path wins",
			cfg:  helixon.RuntimeConfig{Agentrace: helixon.AgentraceConfig{LogPath: "/var/log/x.ndjson"}},
			want: "/var/log/x.ndjson",
		},
		{
			name:     "XDG_STATE_HOME and agent id",
			cfg:      helixon.RuntimeConfig{AgentID: "fleet-1"},
			stateEnv: "/state",
			want:     filepath.Join("/state", "helixon", "agentrace-fleet-1.ndjson"),
		},
		{
			name:     "no agent id falls back to a stable name",
			cfg:      helixon.RuntimeConfig{},
			stateEnv: "/state",
			want:     filepath.Join("/state", "helixon", "agentrace-helixon.ndjson"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.stateEnv != "" {
				t.Setenv("XDG_STATE_HOME", tt.stateEnv)
			}
			if got := agentraceLogPath(tt.cfg); got != tt.want {
				t.Fatalf("agentraceLogPath = %q, want %q", got, tt.want)
			}
		})
	}
}
