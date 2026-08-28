package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

// guardrails bundles the pieces that must be built ONCE and shared between
// tool registration (which happens before Configure) and the executor
// decorators (which are applied during Configure).
//
// serve, task, and repl all used to register the same three builtins and then
// configure no decorators at all: LoopGuardExecutor, TracedExecutor and the
// safety package were in the tree, tested, and wired into nothing. This is the
// single place all three commands now go through, so a guardrail cannot be
// live in one entry point and absent in another.
type guardrails struct {
	runner *sandbox.Runner
	cfg    helixon.RuntimeConfig
}

// newGuardrails builds the sandbox runner (nil when the sandbox is disabled).
// The config is copied deliberately: the guardrails struct must not alias a
// config the caller can still change.
//
//nolint:gocritic // hugeParam: the copy is deliberate, see above
func newGuardrails(cfg helixon.RuntimeConfig) (guardrails, error) {
	runner, err := sandbox.NewRunner(cfg.Sandbox)
	if err != nil {
		return guardrails{}, fmt.Errorf("sandbox: %w", err)
	}
	return guardrails{runner: runner, cfg: cfg}, nil
}

// toolOptions returns the builtin registration options for this runtime.
// file_read/file_write are pinned to the sandbox workspace so their own
// containment check has a root to check against, rather than relying solely
// on the executor gate.
//
//nolint:gocritic // hugeParam: see newGuardrails
func (g guardrails) toolOptions(withWebFetch bool) builtins.Options {
	opts := builtins.Options{
		Shell:     &builtins.ShellConfig{},
		FileRead:  &builtins.FileReadConfig{},
		FileWrite: &builtins.FileWriteConfig{},
	}
	if withWebFetch {
		opts.WebFetch = &builtins.WebFetchConfig{}
	}
	if g.runner != nil {
		root := []string{g.runner.Config().Workspace}
		opts.FileRead.AllowedPaths = root
		opts.FileWrite.AllowedPaths = root
		opts.Verifier = &builtins.VerifierConfig{Runner: g.runner}
	}
	return opts
}

// configOptions returns the executor decorators, innermost first: the
// registry is wrapped by the sandbox gate, then the loop guard, then the
// agentrace recorder — so the trace records loop-guard rejections and
// sandbox refusals as well as successful calls.
//
//nolint:gocritic // hugeParam: see newGuardrails
func (g guardrails) configOptions() []helixon.ConfigOption {
	opts := []helixon.ConfigOption{
		helixon.WithSandboxRunner(g.runner, g.cfg.Sandbox.AllowUnsandboxedHostExecution),
	}
	if g.cfg.LoopGuard.Enabled {
		opts = append(opts, helixon.WithLoopGuard(g.cfg.LoopGuard))
	}
	if g.cfg.Agentrace.Enabled {
		opts = append(opts, helixon.WithAgentrace(tooldispatch.AgentraceConfig{
			LogPath: agentraceLogPath(g.cfg),
			AgentID: g.cfg.AgentID,
			Server:  "helixon",
		}))
	}
	return opts
}

// agentraceLogPath resolves the NDJSON trace path.
//
//nolint:gocritic // hugeParam: see newGuardrails
func agentraceLogPath(cfg helixon.RuntimeConfig) string {
	if cfg.Agentrace.LogPath != "" {
		return cfg.Agentrace.LogPath
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			base = os.TempDir()
		} else {
			base = filepath.Join(home, ".local", "state")
		}
	}
	agentID := cfg.AgentID
	if agentID == "" {
		agentID = "helixon"
	}
	return filepath.Join(base, "helixon", "agentrace-"+agentID+".ndjson")
}
