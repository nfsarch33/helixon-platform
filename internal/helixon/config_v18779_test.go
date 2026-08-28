package helixon_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
)

// liveFleetAgentConfig is the shape of the DEPLOYED wsl1 fleet-agent config
// (secrets elided). Strict decoding was enabled in v18779; if this document
// stops parsing, the live agent crash-loops on its next restart. That is the
// entire reason this fixture is checked in.
const liveFleetAgentConfig = `
agent_id: helixon-fleet-wsl1
system_prompt: |
  You are the wsl1 fleet agent.
session_dsn: "file:/home/agent/.local/share/helixon/fleet-wsl1.db?cache=shared&mode=rwc"
max_iterations: 50
max_tokens: 128000
timeout: 10m
heartbeat_every: 30s

provider:
  kind: openai-compat
  base_url: http://127.0.0.1:8787/v1
  model: MiniMax-M3
  api_key: "${LLM_ROUTER_TOKEN}"
  timeout: 120s

sprintboard:
  url: http://127.0.0.1:9400
  capabilities: "code,test,review,deploy,registry"

registra:
  registry_path: /home/agent/registry.yaml
  bridge_url: http://127.0.0.1:7777
  node_alias: wsl1
`

// TestStrictDecode_LiveFleetAgentConfigStillParses is the crash-loop guard.
func TestStrictDecode_LiveFleetAgentConfigStillParses(t *testing.T) {
	t.Parallel()
	fc, err := helixon.DecodeFileConfig([]byte(liveFleetAgentConfig))
	if err != nil {
		t.Fatalf("the LIVE fleet-agent config must parse under strict decoding: %v", err)
	}
	if fc.AgentID != "helixon-fleet-wsl1" {
		t.Errorf("agent_id = %q", fc.AgentID)
	}
	// The registra block is the key that non-strict decoding silently threw
	// away for months. It must now round-trip.
	if fc.Registra.NodeAlias != "wsl1" || fc.Registra.BridgeURL == "" || fc.Registra.RegistryPath == "" {
		t.Errorf("registra block was not decoded: %+v", fc.Registra)
	}
}

// TestStrictDecode_EveryShippedConfigParses walks the config files this repo
// actually ships, so adding a key to an example without adding the field is a
// build failure rather than a production surprise.
func TestStrictDecode_EveryShippedConfigParses(t *testing.T) {
	t.Parallel()
	roots := []string{
		filepath.Join("..", "..", "examples"),
		filepath.Join("..", "..", "systemd-units"),
		filepath.Join("..", "..", "evidence", "v14571-fleet-agent-live"),
	}
	found := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
				continue
			}
			path := filepath.Join(root, name)
			data, err := os.ReadFile(path) //nolint:gosec // G304 test reads repo fixtures
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			found++
			t.Run(path, func(t *testing.T) {
				t.Parallel()
				if _, err := helixon.DecodeFileConfig(data); err != nil {
					t.Fatalf("shipped config %s does not parse under strict decoding: %v", path, err)
				}
			})
		}
	}
	if found == 0 {
		t.Fatal("no shipped configs were checked; the walk is broken and this guard is worthless")
	}
}

func TestStrictDecode_UnknownKeyIsLoud(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "top-level typo", yaml: "agent_idd: x\n", wantErr: "agent_idd"},
		{name: "nested typo", yaml: "provider:\n  kindd: mock\n", wantErr: "kindd"},
		{
			// The exact class of bug this catches: a security setting that
			// is silently ignored reads as "configured" to the operator.
			name: "sandbox setting typo", yaml: "sandbox:\n  netwrk: none\n", wantErr: "netwrk",
		},
		{name: "empty document is valid", yaml: ""},
		{name: "comment-only document is valid", yaml: "# nothing here\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := helixon.DecodeFileConfig([]byte(tt.yaml))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("DecodeFileConfig = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("DecodeFileConfig = %v, want an error naming %q", err, tt.wantErr)
			}
		})
	}
}

// TestGuardrailDefaults: an absent block means ON, an explicit false means
// off. Getting this backwards would ship a disabled sandbox to every config
// that predates the block.
func TestGuardrailDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		yaml           string
		wantSandbox    bool
		wantLoopGuard  bool
		wantAgentrace  bool
		wantCompletion bool
	}{
		{
			name: "no guardrail blocks at all", yaml: "agent_id: x\n",
			wantSandbox: true, wantLoopGuard: true, wantAgentrace: true, wantCompletion: true,
		},
		{
			name: "explicitly disabled",
			yaml: "sandbox:\n  enabled: false\nloop_guard:\n  enabled: false\nagentrace:\n  enabled: false\ncompletion:\n  enabled: false\n",
		},
		{
			name:        "explicitly enabled",
			yaml:        "sandbox:\n  enabled: true\nloop_guard:\n  enabled: true\nagentrace:\n  enabled: true\ncompletion:\n  enabled: true\n",
			wantSandbox: true, wantLoopGuard: true, wantAgentrace: true, wantCompletion: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc, err := helixon.DecodeFileConfig([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			cfg, err := fc.ToRuntimeConfig()
			if err != nil {
				t.Fatalf("ToRuntimeConfig: %v", err)
			}
			if cfg.Sandbox.Enabled != tt.wantSandbox {
				t.Errorf("sandbox.enabled = %v, want %v", cfg.Sandbox.Enabled, tt.wantSandbox)
			}
			if cfg.LoopGuard.Enabled != tt.wantLoopGuard {
				t.Errorf("loop_guard.enabled = %v, want %v", cfg.LoopGuard.Enabled, tt.wantLoopGuard)
			}
			if cfg.Agentrace.Enabled != tt.wantAgentrace {
				t.Errorf("agentrace.enabled = %v, want %v", cfg.Agentrace.Enabled, tt.wantAgentrace)
			}
			if cfg.Completion.Enabled != tt.wantCompletion {
				t.Errorf("completion.enabled = %v, want %v", cfg.Completion.Enabled, tt.wantCompletion)
			}
			if cfg.Sandbox.AllowUnsandboxedHostExecution {
				t.Error("allow_unsandboxed_host_execution must never default to true")
			}
		})
	}
}

func TestSandboxBlock_ThreadsThrough(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	yaml := "" +
		"sandbox:\n" +
		"  image: localhost/custom:1\n" +
		"  network: bridge\n" +
		"  workspace: " + dir + "\n" +
		"  workspace_access: ro\n" +
		"  memory_limit: 256m\n" +
		"  pids_limit: 32\n" +
		"  timeout: 45s\n" +
		"  max_output_bytes: 2048\n" +
		"  allowed_commands: [echo]\n" +
		"  env:\n" +
		"    CI: \"1\"\n" +
		"  binds:\n" +
		"    - {host: " + dir + ", container: /opt/x, mode: rw}\n"

	fc, err := helixon.DecodeFileConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	cfg, err := fc.ToRuntimeConfig()
	if err != nil {
		t.Fatalf("ToRuntimeConfig: %v", err)
	}
	s := cfg.Sandbox
	if s.Image != "localhost/custom:1" || s.Network != "bridge" || s.WorkspaceAccess != sandbox.WorkspaceRO {
		t.Fatalf("sandbox block did not thread through: %+v", s)
	}
	if s.MemoryLimit != "256m" || s.PidsLimit != 32 || s.MaxOutputBytes != 2048 || s.Timeout.String() != "45s" {
		t.Fatalf("limits did not thread through: %+v", s)
	}
	if len(s.Binds) != 1 || !s.Binds[0].ReadWrite || s.Binds[0].Container != "/opt/x" {
		t.Fatalf("binds did not thread through: %+v", s.Binds)
	}
	if s.Env["CI"] != "1" {
		t.Fatalf("env did not thread through: %+v", s.Env)
	}
	// And the runner must accept it (network=bridge with a read-only
	// workspace is the permitted combination).
	if _, err := sandbox.NewRunner(s); err != nil {
		t.Fatalf("NewRunner on the threaded config: %v", err)
	}
}

func TestSandboxBlock_InvalidValuesAreRejected(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, yaml, wantErr string }{
		{name: "bad bind mode", yaml: "sandbox:\n  binds:\n    - {host: /tmp, container: /x, mode: rwx}\n", wantErr: "must be ro or rw"},
		{name: "bad timeout", yaml: "sandbox:\n  timeout: forever\n", wantErr: "sandbox.timeout"},
		{name: "bad loop guard window", yaml: "loop_guard:\n  window: forever\n", wantErr: "loop_guard.window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc, err := helixon.DecodeFileConfig([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, err := fc.ToRuntimeConfig(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ToRuntimeConfig = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultRuntimeConfig(t *testing.T) {
	t.Parallel()
	cfg, err := helixon.DefaultRuntimeConfig()
	if err != nil {
		t.Fatalf("DefaultRuntimeConfig: %v", err)
	}
	if !cfg.Sandbox.Enabled || !cfg.LoopGuard.Enabled || !cfg.Completion.Enabled {
		t.Fatalf("the config-less path must still get every guardrail: %+v", cfg)
	}
	if cfg.LoopGuard.Threshold <= 0 || cfg.LoopGuard.Window <= 0 {
		t.Fatalf("loop guard defaults not applied: %+v", cfg.LoopGuard)
	}
}
