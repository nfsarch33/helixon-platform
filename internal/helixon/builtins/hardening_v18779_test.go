package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestShellAllowList_NoExecutionPrimitives: before v18779 the default
// allow-list contained git, go and make. Each of those runs arbitrary code
// (`git -c core.pager=<cmd>`, `go run`, any make target), so the list that
// claimed to be "deliberately restrictive" was a general execution primitive
// on the fleet host.
func TestShellAllowList_NoExecutionPrimitives(t *testing.T) {
	t.Parallel()
	for _, banned := range []string{"git", "go", "make"} {
		for _, allowed := range ShellAllowedCommands {
			if allowed == banned {
				t.Errorf("%q is back on the default shell allow-list", banned)
			}
		}
	}
	def := ShellTool(ShellConfig{})
	if _, err := def.Handler(context.Background(), map[string]any{"command": "git", "args": []any{"log"}}); err == nil {
		t.Error("git must be rejected by the default allow-list")
	}
}

// TestShellTool_ArgvIsValidated: the arguments used to reach exec entirely
// unvalidated, so an allow-listed `find` could carry -exec or -delete and do
// whatever it liked.
//
// SAFETY: every destructive case below is aimed at a scratch directory this
// test owns, never at "." — the shell tool has no working-directory jail, so
// it inherits the test binary's cwd, which is the PACKAGE directory. The
// first version of this test used "." and, the moment the mutation harness
// removed ValidateArgv to check that the guard was load-bearing, `find .
// -delete` ran for real and deleted the package's source files. The guard is
// evidently real; the test must not depend on it to avoid damage.
func TestShellTool_ArgvIsValidated(t *testing.T) {
	t.Parallel()
	def := ShellTool(ShellConfig{AllowedCommands: []string{"find", "echo", "grep"}, Timeout: 5 * time.Second})
	tests := []struct {
		name    string
		command string
		args    func(scratch string) []any
		wantErr string
	}{
		{
			name: "find -exec", command: "find", wantErr: "not permitted",
			args: func(s string) []any { return []any{s, "-exec", "id", ";"} },
		},
		{
			name: "find -execdir", command: "find", wantErr: "not permitted",
			args: func(s string) []any { return []any{s, "-execdir", "id", ";"} },
		},
		{
			name: "find -delete", command: "find", wantErr: "not permitted",
			args: func(s string) []any { return []any{s, "-delete"} },
		},
		{
			name: "grep -f", command: "grep", wantErr: "not permitted",
			args: func(s string) []any { return []any{"-f", "/etc/shadow", s} },
		},
		{
			name: "NUL byte", command: "echo", wantErr: "NUL byte",
			args: func(string) []any { return []any{"a\x00b"} },
		},
		{
			name: "non-string arg", command: "echo", wantErr: "must be strings",
			args: func(string) []any { return []any{1} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scratch := t.TempDir()
			canary := filepath.Join(scratch, "canary.txt")
			if err := os.WriteFile(canary, []byte("x"), 0o600); err != nil {
				t.Fatalf("write canary: %v", err)
			}
			_, err := def.Handler(context.Background(), map[string]any{
				"command": tt.command,
				"args":    tt.args(scratch),
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Handler() = %v, want an error containing %q", err, tt.wantErr)
			}
			// Nothing may have executed: the guard runs before exec.
			if _, statErr := os.Stat(canary); statErr != nil {
				t.Fatalf("the command executed despite being rejected: %v", statErr)
			}
		})
	}
}

// TestResolveAllowedPath_ContainmentReplacesHasPrefix covers the three
// escapes the old strings.HasPrefix test let through.
func TestResolveAllowedPath_ContainmentReplacesHasPrefix(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canon, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "inside", path: filepath.Join(canon, "a.txt")},
		{name: "root itself", path: canon},
		{name: "traversal out", path: filepath.Join(canon, "..", "a.txt"), wantErr: true},
		{name: "prefix sibling", path: canon + "-evil/a.txt", wantErr: true},
		{name: "unrelated absolute", path: "/etc/passwd", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveAllowedPath(tt.path, []string{canon})
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveAllowedPath(%q) err = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
			if !tt.wantErr && strings.Contains(got, "..") {
				t.Fatalf("the returned path must be canonical; got %q", got)
			}
		})
	}
}

// TestFileTools_CannotEscapeTheirAllowedRoot drives the escape through the
// actual tool handlers, not just the helper.
func TestFileTools_CannotEscapeTheirAllowedRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	read := FileReadTool(FileReadConfig{AllowedPaths: []string{root}})
	write := FileWriteTool(FileWriteConfig{AllowedPaths: []string{root}})
	ctx := context.Background()

	traversal := filepath.Join(root, "..", filepath.Base(outside), "secret.txt")
	if out, err := read.Handler(ctx, map[string]any{"path": traversal}); err == nil {
		t.Fatalf("file_read escaped its root and returned %q", out)
	}
	if _, err := write.Handler(ctx, map[string]any{"path": traversal, "content": "x"}); err == nil {
		t.Fatal("file_write escaped its root")
	}

	// A symlink planted inside the root is the case a prefix check cannot
	// see: the path string is legitimately under the root.
	link := filepath.Join(root, "innocent")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if out, err := read.Handler(ctx, map[string]any{"path": filepath.Join(link, "secret.txt")}); err == nil {
		t.Fatalf("file_read followed a symlink out of its root and returned %q", out)
	}

	// And the legitimate case still works.
	inside := filepath.Join(root, "notes.txt")
	if _, err := write.Handler(ctx, map[string]any{"path": inside, "content": "hello"}); err != nil {
		t.Fatalf("file_write inside the root must work: %v", err)
	}
	got, err := read.Handler(ctx, map[string]any{"path": inside})
	if err != nil || got != "hello" {
		t.Fatalf("file_read inside the root = %q, %v", got, err)
	}
}
