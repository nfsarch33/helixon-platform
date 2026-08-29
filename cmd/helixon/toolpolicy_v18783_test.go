package main

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

// namespacedTools are the tools helixon.RegisterBuiltinTools adds when a
// memory backend and a sprintboard client are configured. They are listed
// literally because reaching them needs a runtime with both wired, and the
// point of this test is the classification, not the wiring. Their source of
// truth is internal/helixon/registry.go.
var namespacedTools = []string{
	"memory.search", "memory.write", "memory.read",
	"sprintboard.claim_ticket", "sprintboard.complete_ticket",
}

// allRegisterableTools returns every tool name this repository can put into a
// registry: the full builtins set (every Options field populated) plus the
// namespaced set.
func allRegisterableTools(t *testing.T) []string {
	t.Helper()
	cfg := serveTestConfig(t)
	g, err := newGuardrails(cfg)
	if err != nil {
		t.Fatalf("newGuardrails: %v", err)
	}
	opts := g.toolOptions(true)
	// Populate the fields the CLI does not use, so a tool that only some
	// deployment registers is still classified.
	opts.Memory = &memory.HybridSearcher{}
	opts.Sprintboard = &controlplane.SprintboardClient{}
	opts.Autoresearch = &builtins.AutoresearchConfig{}

	names := make([]string, 0, 16)
	for _, d := range opts.Defs() {
		names = append(names, d.Name)
	}
	names = append(names, namespacedTools...)
	sort.Strings(names)
	return names
}

// TestEveryRegisterableToolIsExplicitlyClassified is the v18783 sweep the
// web_fetch defect asked for: not "is web_fetch handled" but "is anything
// else falling through the same way".
//
// It reads DefaultPolicy's map directly rather than calling For(), because
// For() answers with the default for a tool that was never listed — which is
// exactly the state that has to be detectable. A new builtin registered
// without a policy entry fails here.
func TestEveryRegisterableToolIsExplicitlyClassified(t *testing.T) {
	t.Parallel()
	policy := sandbox.DefaultPolicy()

	var table strings.Builder
	table.WriteString("\ntool -> disposition\n")
	for _, name := range allRegisterableTools(t) {
		d, listed := policy.Tools[name]
		if !listed {
			t.Errorf("tool %q has no entry in sandbox.DefaultPolicy; it falls through to the default", name)
			table.WriteString("  " + name + " -> UNLISTED (falls through to " + policy.Default.String() + ")\n")
			continue
		}
		table.WriteString("  " + name + " -> " + d.String() + "\n")
	}
	t.Log(table.String())
}

// TestServeRegistersNoUnclassifiedTool is the same check against the tool set
// `helixon serve` actually stands up, so a registration added to the serve
// path (rather than to builtins.Options) is caught too.
func TestServeRegistersNoUnclassifiedTool(t *testing.T) {
	t.Parallel()
	cfg := serveTestConfig(t)
	rt := helixon.NewRuntime(llm.NewMockProvider(), cfg)
	if err := initAndConfigureRuntime(context.Background(), rt, cfg, ""); err != nil {
		t.Fatalf("initAndConfigureRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	policy := sandbox.DefaultPolicy()
	names := rt.Registry().Names()
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("serve registered no tools at all; this check would pass vacuously")
	}
	for _, name := range names {
		if _, listed := policy.Tools[name]; !listed {
			t.Errorf("serve registers %q, which sandbox.DefaultPolicy does not classify", name)
		}
	}
	t.Logf("serve tool surface: %s", strings.Join(names, ", "))
}

// TestGuardrails_ShellIsAlwaysJailed is the wiring assertion for the v18783
// cwd jail. The escape hatch is the configuration that installs no sandbox
// gate, so it is precisely the one where an unset WorkDir would leave the
// shell tool running in whatever directory the process started in.
func TestGuardrails_ShellIsAlwaysJailed(t *testing.T) {
	t.Parallel()
	base := serveTestConfig(t)

	t.Run("sandbox enabled", func(t *testing.T) {
		t.Parallel()
		cfg := base
		cfg.Sandbox.Workspace = t.TempDir()
		g, err := newGuardrails(cfg)
		if err != nil {
			t.Fatalf("newGuardrails: %v", err)
		}
		if g.runner == nil {
			t.Fatal("expected a runner with the sandbox enabled")
		}
		if got := g.toolOptions(false).Shell.WorkDir; got != g.runner.Config().Workspace {
			t.Errorf("shell WorkDir = %q, want the runner workspace %q", got, g.runner.Config().Workspace)
		}
	})

	t.Run("host execution escape hatch", func(t *testing.T) {
		t.Parallel()
		ws := t.TempDir()
		cfg := base
		cfg.Sandbox = sandbox.Config{
			Enabled:                       false,
			AllowUnsandboxedHostExecution: true,
			Workspace:                     ws,
		}
		g, err := newGuardrails(cfg)
		if err != nil {
			t.Fatalf("newGuardrails: %v", err)
		}
		if g.runner != nil {
			t.Fatal("a disabled sandbox must not build a runner")
		}
		if got := g.toolOptions(false).Shell.WorkDir; got != ws {
			t.Errorf("shell WorkDir = %q, want the configured workspace %q; the ungated path is the one that needs the jail", got, ws)
		}
	})

	t.Run("no workspace configured falls back to the process cwd", func(t *testing.T) {
		t.Parallel()
		cfg := base
		cfg.Sandbox = sandbox.Config{Enabled: false, AllowUnsandboxedHostExecution: true}
		g, err := newGuardrails(cfg)
		if err != nil {
			t.Fatalf("newGuardrails: %v", err)
		}
		if got := g.toolOptions(false).Shell.WorkDir; got == "" {
			t.Error("shell WorkDir must never be empty: empty means unjailed")
		}
	})
}
