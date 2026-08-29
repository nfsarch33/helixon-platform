package builtins_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
)

// jailedShell returns the shell tool wired to a fresh workspace containing one
// readable file, plus the workspace path.
//
// The handler is invoked under an explicit deadline. ShellConfig.Timeout is
// applied by tooldispatch.Registry, not by the handler itself, so a test that
// calls the handler directly has no bound at all — and these tests exist
// precisely to be run against a BROKEN jail, where the refused command
// executes for real. Without the deadline, mutating the containment check to a
// no-op turns `find` on an absolute path into a full-filesystem walk that
// hangs the suite instead of failing it. A test whose failure mode is a hang
// reports nothing.
func jailedShell(t *testing.T) (func(map[string]any) (string, error), string) {
	t.Helper()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("in-workspace\n"), 0o600); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	def := builtins.ShellTool(builtins.ShellConfig{WorkDir: ws, Timeout: 10 * time.Second})
	return func(args map[string]any) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return def.Handler(ctx, args)
	}, ws
}

func argv(items ...string) []any {
	out := make([]any, 0, len(items))
	for _, s := range items {
		out = append(out, s)
	}
	return out
}

// TestShellTool_HostExecutionIsJailedToTheWorkspace is the v18783 defect-3
// assertion. The escape hatch (allow_unsandboxed_host_execution) installs no
// sandbox gate, so this handler runs for real on the host; before the jail it
// ran with the parent process's working directory and could address anything
// the agent's uid could.
//
// Removing either half of the fix — the sandbox.ContainArgv call or the
// cmd.Dir assignment — fails this test.
func TestShellTool_HostExecutionIsJailedToTheWorkspace(t *testing.T) {
	t.Parallel()
	run, _ := jailedShell(t)

	tests := []struct {
		name string
		args map[string]any
	}{
		{"absolute path outside", map[string]any{"command": "cat", "args": argv("/etc/passwd")}},
		{"parent directory", map[string]any{"command": "ls", "args": argv("..")}},
		{"traversal", map[string]any{"command": "cat", "args": argv("../../etc/hosts")}},
		// A small directory on purpose: if the jail regresses this command
		// runs for real, and the point is to fail fast, not to walk a
		// filesystem.
		{"absolute path search outside", map[string]any{"command": "find", "args": argv("/etc", "-name", "hosts")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := run(tt.args)
			if err == nil {
				t.Fatalf("%v was allowed to leave the workspace; output = %q", tt.args, out)
			}
			if !strings.Contains(err.Error(), "outside the workspace") {
				t.Errorf("error = %v, want it to name the containment failure", err)
			}
		})
	}
}

// TestShellTool_JailedShellStillDoesUsefulWork is the POSITIVE CONTROL for
// the test above, and the reason both exist.
//
// A jail that refuses every command passes every escape assertion while
// leaving the agent unable to run anything — the failure mode this estate has
// already shipped once, when a sandbox passed eight containment tests while
// blocking its own Go toolchain. These calls must succeed, and they fail if
// the containment rule is widened into a blanket refusal.
func TestShellTool_JailedShellStillDoesUsefulWork(t *testing.T) {
	t.Parallel()
	run, ws := jailedShell(t)

	t.Run("relative read inside the workspace", func(t *testing.T) {
		t.Parallel()
		out, err := run(map[string]any{"command": "cat", "args": argv("notes.txt")})
		if err != nil {
			t.Fatalf("reading a workspace file must work: %v", err)
		}
		if !strings.Contains(out, "in-workspace") {
			t.Errorf("output = %q, want the file contents", out)
		}
	})

	t.Run("absolute path inside the workspace", func(t *testing.T) {
		t.Parallel()
		out, err := run(map[string]any{"command": "cat", "args": argv(filepath.Join(ws, "notes.txt"))})
		if err != nil {
			t.Fatalf("an absolute path INSIDE the workspace must work: %v", err)
		}
		if !strings.Contains(out, "in-workspace") {
			t.Errorf("output = %q, want the file contents", out)
		}
	})

	t.Run("listing the workspace", func(t *testing.T) {
		t.Parallel()
		out, err := run(map[string]any{"command": "ls", "args": argv("-la", ".")})
		if err != nil {
			t.Fatalf("listing the workspace must work: %v", err)
		}
		if !strings.Contains(out, "notes.txt") {
			t.Errorf("output = %q, want the seeded file", out)
		}
	})

	t.Run("a pattern argument is not mistaken for a path", func(t *testing.T) {
		t.Parallel()
		out, err := run(map[string]any{"command": "grep", "args": argv("in-workspace", "notes.txt")})
		if err != nil {
			t.Fatalf("grep with a pattern argument must work: %v", err)
		}
		if !strings.Contains(out, "in-workspace") {
			t.Errorf("output = %q, want the matching line", out)
		}
	})
}

// TestShellTool_RunsInTheWorkspaceNotTheProcessCwd proves the cwd half of the
// jail specifically. Argument containment alone would still leave "." meaning
// the agent's own working directory, which is how `find . -delete` deleted
// something real.
func TestShellTool_RunsInTheWorkspaceNotTheProcessCwd(t *testing.T) {
	t.Parallel()
	run, ws := jailedShell(t)

	out, err := run(map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("resolve reported cwd %q: %v", out, err)
	}
	want, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	if got != want {
		t.Fatalf("the command ran in %q, want the workspace %q", got, want)
	}

	procCwd, err := os.Getwd()
	if err == nil {
		if resolved, rErr := filepath.EvalSymlinks(procCwd); rErr == nil && got == resolved {
			t.Fatal("the command inherited the test process's working directory; the cwd jail is not applied")
		}
	}
}
