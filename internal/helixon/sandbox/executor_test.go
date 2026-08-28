package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// recordingInner is a stand-in for *tooldispatch.Registry.
type recordingInner struct {
	calls  []string
	args   []string
	result string
	err    error
	tools  []llm.Tool
}

func (r *recordingInner) Execute(_ context.Context, name, argsJSON string) (string, error) {
	r.calls = append(r.calls, name)
	r.args = append(r.args, argsJSON)
	return r.result, r.err
}

func (r *recordingInner) Available() []llm.Tool { return r.tools }

func newTestExecutor(t *testing.T, policy Policy, mutate func(Config) Config) (*Executor, *recordingInner, *fakeEngine) {
	t.Helper()
	runner, fe := newTestRunner(t, mutate)
	inner := &recordingInner{result: "inner-result", tools: []llm.Tool{{Type: "function"}}}
	exec, err := NewExecutor(inner, runner, policy, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return exec, inner, fe
}

func TestDispositionOrdering(t *testing.T) {
	t.Parallel()
	// The ordering is what makes Layer monotonic; assert it explicitly so a
	// reordering of the const block is a test failure, not a silent
	// widening of every policy in the tree.
	if DispositionAllow >= DispositionPathGuard ||
		DispositionPathGuard >= DispositionSandbox ||
		DispositionSandbox >= DispositionDeny {
		t.Fatal("dispositions must be ordered least- to most-restrictive")
	}
	for d, want := range map[Disposition]string{
		DispositionAllow: "allow", DispositionPathGuard: "path_guard",
		DispositionSandbox: "sandbox", DispositionDeny: "deny", Disposition(99): "unknown",
	} {
		if got := d.String(); got != want {
			t.Errorf("Disposition(%d).String() = %q, want %q", d, got, want)
		}
	}
}

// TestPolicyLayer_OnlyRestricts is the monotonicity property: no later layer
// can re-grant what an earlier one took away.
func TestPolicyLayer_OnlyRestricts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		base Policy
		next Policy
		want map[string]Disposition
	}{
		{
			name: "next tightens a tool",
			base: Policy{Default: DispositionAllow, Tools: map[string]Disposition{"shell": DispositionPathGuard}},
			next: Policy{Default: DispositionAllow, Tools: map[string]Disposition{"shell": DispositionDeny}},
			want: map[string]Disposition{"shell": DispositionDeny},
		},
		{
			name: "next tries to loosen a tool and cannot",
			base: Policy{Default: DispositionAllow, Tools: map[string]Disposition{"shell": DispositionSandbox}},
			next: Policy{Default: DispositionAllow, Tools: map[string]Disposition{"shell": DispositionAllow}},
			want: map[string]Disposition{"shell": DispositionSandbox},
		},
		{
			name: "next tries to loosen the default and cannot",
			base: Policy{Default: DispositionDeny},
			next: Policy{Default: DispositionAllow, Tools: map[string]Disposition{"memory": DispositionAllow}},
			want: map[string]Disposition{"memory": DispositionDeny, "anything": DispositionDeny},
		},
		{
			name: "a tool only next names inherits base's default as its floor",
			base: Policy{Default: DispositionSandbox},
			next: Policy{Default: DispositionAllow, Tools: map[string]Disposition{"web_fetch": DispositionAllow}},
			want: map[string]Disposition{"web_fetch": DispositionSandbox},
		},
		{
			name: "a tool only base names keeps its own value",
			base: Policy{Default: DispositionAllow, Tools: map[string]Disposition{"file_write": DispositionPathGuard}},
			next: Policy{Default: DispositionAllow},
			want: map[string]Disposition{"file_write": DispositionPathGuard},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.base.Layer(tt.next)
			for tool, want := range tt.want {
				if d := got.For(tool); d != want {
					t.Errorf("For(%q) = %s, want %s", tool, d, want)
				}
			}
			// Universal property: the layered result is never looser than
			// either input, for any tool either input mentions.
			for _, tool := range []string{"shell", "file_write", "memory", "web_fetch", "unknown_tool"} {
				if d := got.For(tool); d < tt.base.For(tool) || d < tt.next.For(tool) {
					t.Errorf("Layer loosened %q: got %s, base %s, next %s", tool, d, tt.base.For(tool), tt.next.For(tool))
				}
			}
		})
	}
}

func TestDefaultPolicy_ClassifiesBuiltins(t *testing.T) {
	t.Parallel()
	p := DefaultPolicy()
	want := map[string]Disposition{
		"shell":        DispositionSandbox,
		"file_read":    DispositionPathGuard,
		"file_write":   DispositionPathGuard,
		"verifier_run": DispositionAllow,
		"anything_new": DispositionPathGuard, // the default
	}
	for tool, d := range want {
		if got := p.For(tool); got != d {
			t.Errorf("DefaultPolicy().For(%q) = %s, want %s", tool, got, d)
		}
	}
}

// TestExecute_ShellIsInterceptedNotForwarded proves the gate replaces the
// host-side handler rather than merely inspecting the call: the inner
// executor (whose shell handler runs on the host) is never reached.
func TestExecute_ShellIsInterceptedNotForwarded(t *testing.T) {
	t.Parallel()
	e, inner, fe := newTestExecutor(t, DefaultPolicy(), nil)
	fe.runFn = func(_ context.Context, _ []string, out io.Writer) (int, error) {
		_, _ = out.Write([]byte("sandboxed output"))
		return 0, nil
	}
	got, err := e.Execute(context.Background(), "shell", `{"command":"echo","args":["hi"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "sandboxed output" {
		t.Fatalf("output = %q, want the sandboxed output", got)
	}
	if len(inner.calls) != 0 {
		t.Fatalf("the host-side shell handler must never be reached; inner saw %v", inner.calls)
	}
}

func TestExecute_SandboxedToolArgumentErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{name: "missing command", args: `{"args":["hi"]}`, wantErr: "require a string \"command\""},
		{name: "args not an array", args: `{"command":"echo","args":"hi"}`, wantErr: "must be an array"},
		{name: "args entry not a string", args: `{"command":"echo","args":[1]}`, wantErr: "must be a string"},
		{name: "malformed json", args: `{`, wantErr: "decode arguments"},
		{name: "not allow-listed", args: `{"command":"git","args":["log"]}`, wantErr: "not allow-listed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, _, _ := newTestExecutor(t, DefaultPolicy(), nil)
			if _, err := e.Execute(context.Background(), "shell", tt.args); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Execute() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestExecute_SandboxedNonZeroExitIsAnError(t *testing.T) {
	t.Parallel()
	e, _, fe := newTestExecutor(t, DefaultPolicy(), nil)
	fe.runFn = func(_ context.Context, _ []string, out io.Writer) (int, error) {
		_, _ = out.Write([]byte("boom"))
		return 2, errors.New("exit status 2")
	}
	out, err := e.Execute(context.Background(), "shell", `{"command":"echo"}`)
	if err == nil {
		t.Fatal("a failing sandboxed command must surface as a tool error")
	}
	if out != "boom" {
		t.Fatalf("the output must still reach the model; got %q", out)
	}
}

// TestExecute_PathGuardRejectsTraversal is the file_write escape case.
func TestExecute_PathGuardRejectsTraversal(t *testing.T) {
	// No t.Parallel: the subtests share `inner` and assert on its call
	// count, so they must run in order.
	workspace := t.TempDir()
	e, inner, _ := newTestExecutor(t, DefaultPolicy(), func(c Config) Config {
		c.Workspace = workspace
		return c
	})

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "inside the workspace", path: filepath.Join(workspace, "notes.txt")},
		{name: "new file inside the workspace", path: filepath.Join(workspace, "sub", "new.txt")},
		{name: "dot-dot traversal", path: filepath.Join(workspace, "..", "escape.txt"), wantErr: true},
		{name: "absolute path elsewhere", path: "/etc/passwd", wantErr: true},
		{name: "sibling with the same prefix", path: workspace + "-evil/x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No t.Parallel: the subtests share `inner` and assert on its
			// call count, so they must run in order.
			payload, _ := json.Marshal(map[string]any{"path": tt.path, "content": "x"})
			before := len(inner.calls)
			_, err := e.Execute(context.Background(), "file_write", string(payload))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("path %q must be rejected", tt.path)
				}
				if !errors.Is(err, ErrPathEscape) {
					t.Fatalf("error must be ErrPathEscape, got %v", err)
				}
				if len(inner.calls) != before {
					t.Fatal("a rejected path must never reach the inner handler")
				}
				return
			}
			if err != nil {
				t.Fatalf("path %q should be allowed: %v", tt.path, err)
			}
			if len(inner.calls) != before+1 {
				t.Fatal("an allowed path must be forwarded to the inner handler")
			}
		})
	}
}

// TestExecute_PathGuardRewritesToTheCanonicalPath: the inner handler must
// open exactly what the guard approved, not re-resolve the caller's string.
func TestExecute_PathGuardRewritesToTheCanonicalPath(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "a", "b"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	e, inner, _ := newTestExecutor(t, DefaultPolicy(), func(c Config) Config {
		c.Workspace = workspace
		return c
	})
	messy := filepath.Join(workspace, "a", "b", "..", "..", "a", "file.txt")
	payload, _ := json.Marshal(map[string]any{"path": messy})
	if _, err := e.Execute(context.Background(), "file_read", string(payload)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var forwarded map[string]any
	if err := json.Unmarshal([]byte(inner.args[0]), &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded args: %v", err)
	}
	if got := forwarded["path"].(string); strings.Contains(got, "..") {
		t.Fatalf("forwarded path was not canonicalized: %q", got)
	}
}

func TestExecute_PathGuardIgnoresNonPathTools(t *testing.T) {
	t.Parallel()
	e, inner, _ := newTestExecutor(t, DefaultPolicy(), nil)
	if _, err := e.Execute(context.Background(), "memory", `{"op":"search","query":"x"}`); err != nil {
		t.Fatalf("a tool with no path arguments must pass through: %v", err)
	}
	if len(inner.calls) != 1 || inner.args[0] != `{"op":"search","query":"x"}` {
		t.Fatalf("payload should be forwarded verbatim when nothing was rewritten; got %v", inner.args)
	}
}

func TestExecute_DenyAndUnknownDisposition(t *testing.T) {
	t.Parallel()
	e, inner, _ := newTestExecutor(t, Policy{
		Default: DispositionDeny,
		Tools:   map[string]Disposition{"weird": Disposition(99)},
	}, nil)

	if _, err := e.Execute(context.Background(), "anything", "{}"); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("an unlisted tool under a deny default must be denied, got %v", err)
	}
	if _, err := e.Execute(context.Background(), "weird", "{}"); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("an unknown disposition must fail closed, got %v", err)
	}
	if len(inner.calls) != 0 {
		t.Fatalf("denied tools must never reach the inner executor; saw %v", inner.calls)
	}
}

func TestExecute_AllowForwardsVerbatim(t *testing.T) {
	t.Parallel()
	e, inner, _ := newTestExecutor(t, DefaultPolicy(), nil)
	if _, err := e.Execute(context.Background(), "verifier_run", `{"check":"go_test"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "verifier_run" {
		t.Fatalf("verifier_run must be forwarded; inner saw %v", inner.calls)
	}
}

func TestNewExecutor_Guards(t *testing.T) {
	t.Parallel()
	runner, _ := newTestRunner(t, nil)
	if _, err := NewExecutor(nil, runner, DefaultPolicy(), nil); err == nil {
		t.Fatal("a nil inner executor must be rejected")
	}
	if _, err := NewExecutor(&recordingInner{}, nil, DefaultPolicy(), nil); err == nil {
		t.Fatal("a nil runner must be rejected: a gate with nothing behind it is not a gate")
	}
	// An empty policy falls back to the hardened default rather than
	// silently allowing everything.
	e, err := NewExecutor(&recordingInner{}, runner, Policy{}, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if got := e.Policy().For("shell"); got != DispositionSandbox {
		t.Fatalf("an empty policy must fall back to DefaultPolicy; shell = %s", got)
	}
}

func TestExecutor_AvailableProxies(t *testing.T) {
	t.Parallel()
	e, inner, _ := newTestExecutor(t, DefaultPolicy(), nil)
	if len(e.Available()) != len(inner.tools) {
		t.Fatal("Available must proxy to the inner executor")
	}
}
