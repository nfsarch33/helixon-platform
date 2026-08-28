package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine records invocations and replays scripted outcomes so the argv
// construction and the error mapping can be tested without a container.
type fakeEngine struct {
	mu       sync.Mutex
	calls    [][]string
	lookErr  error
	imageErr error // non-nil => `image exists` reports missing
	runFn    func(ctx context.Context, args []string, out io.Writer) (int, error)
}

func (f *fakeEngine) lookPath(string) error { return f.lookErr }

func (f *fakeEngine) run(ctx context.Context, _ string, args []string, out io.Writer) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
	if len(args) >= 2 && args[0] == "image" && args[1] == "exists" {
		if f.imageErr != nil {
			return 1, f.imageErr
		}
		return 0, nil
	}
	if f.runFn != nil {
		return f.runFn(ctx, args, out)
	}
	return 0, nil
}

func newTestRunner(t *testing.T, mutate func(c Config) Config) (*Runner, *fakeEngine) {
	t.Helper()
	cfg := Config{Enabled: true, Workspace: t.TempDir()}.Normalize(t.TempDir())
	if mutate != nil {
		cfg = mutate(cfg)
	}
	validated, err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	fe := &fakeEngine{}
	return &Runner{cfg: validated, engine: fe}, fe
}

// TestEngineArgs_HardeningFlags asserts every containment flag individually.
// Each entry here is a security control, and each has a matching mutation
// note: deleting the flag from EngineArgs fails exactly this subtest.
func TestEngineArgs_HardeningFlags(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, nil)
	args := r.EngineArgs(Spec{Command: "echo", Args: []string{"hi"}})
	joined := strings.Join(args, " ")

	required := []struct {
		flag string
		why  string
	}{
		{"--network=none", "network isolation; without it the container can exfiltrate"},
		{"--read-only", "read-only root filesystem"},
		{"--cap-drop=ALL", "no Linux capabilities"},
		{"--security-opt=no-new-privileges", "no setuid escalation"},
		{"--user=" + DefaultUser, "non-root user"},
		{"--memory=" + DefaultMemoryLimit, "memory ceiling"},
		{"--pids-limit=256", "pid ceiling (fork-bomb bound)"},
		{"--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=" + DefaultTmpfsSize, "scratch tmpfs, non-executable"},
		{"--rm", "container is removed after the run"},
	}
	for _, req := range required {
		t.Run(req.flag, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(joined, req.flag) {
				t.Fatalf("missing %s (%s); argv was: %s", req.flag, req.why, joined)
			}
		})
	}
	if args[0] != "run" {
		t.Fatalf("first argument must be `run`, got %q", args[0])
	}
	if got := args[len(args)-2]; got != "echo" {
		t.Fatalf("command must follow the image; argv tail = %v", args[len(args)-3:])
	}
	if got := args[len(args)-1]; got != "hi" {
		t.Fatalf("argument must be last; got %q", got)
	}
}

func TestEngineArgs_WorkspaceModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		access  WorkspaceAccess
		network string
		want    string
		absent  bool
	}{
		{name: "rw workspace", access: WorkspaceRW, network: "none", want: ":" + DefaultWorkspaceMount + ":rw"},
		{name: "ro workspace", access: WorkspaceRO, network: "none", want: ":" + DefaultWorkspaceMount + ":ro"},
		{name: "no workspace mount at all", access: WorkspaceNone, network: "none", absent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, _ := newTestRunner(t, func(c Config) Config {
				c.WorkspaceAccess = tt.access
				c.Network = tt.network
				return c
			})
			joined := strings.Join(r.EngineArgs(Spec{Command: "echo"}), " ")
			hasVolume := strings.Contains(joined, "--volume=")
			if tt.absent {
				if hasVolume {
					t.Fatalf("workspace access=none must mount nothing; argv: %s", joined)
				}
				return
			}
			if !strings.Contains(joined, tt.want) {
				t.Fatalf("expected %q in argv: %s", tt.want, joined)
			}
		})
	}
}

func TestEngineArgs_BindsAndEnvAreExplicitOnly(t *testing.T) {
	t.Parallel()
	bindDir := t.TempDir()
	r, _ := newTestRunner(t, func(c Config) Config {
		c.Binds = []Bind{{Host: bindDir, Container: "/opt/tools"}}
		c.Env = map[string]string{"CI": "1", "AAA": "2"}
		return c
	})
	args := r.EngineArgs(Spec{Command: "echo"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/opt/tools:ro") {
		t.Fatalf("bind must default to read-only; argv: %s", joined)
	}
	if !strings.Contains(joined, "--env=AAA=2") || !strings.Contains(joined, "--env=CI=1") {
		t.Fatalf("explicit env missing; argv: %s", joined)
	}
	// Env order must be stable so the argv is reproducible in traces.
	if strings.Index(joined, "--env=AAA=2") > strings.Index(joined, "--env=CI=1") {
		t.Fatalf("env vars must be emitted in sorted order; argv: %s", joined)
	}
	// The host environment is never forwarded wholesale.
	for _, a := range args {
		if a == "--env-host" || a == "--env-file" {
			t.Fatalf("the host environment must never be forwarded; found %q", a)
		}
	}
}

func TestEngineArgs_ReadWriteBind(t *testing.T) {
	t.Parallel()
	bindDir := t.TempDir()
	r, _ := newTestRunner(t, func(c Config) Config {
		c.Binds = []Bind{{Host: bindDir, Container: "/opt/rw", ReadWrite: true}}
		return c
	})
	if !strings.Contains(strings.Join(r.EngineArgs(Spec{Command: "echo"}), " "), "/opt/rw:rw") {
		t.Fatal("an explicit read-write bind should be emitted as :rw")
	}
}

// TestPreflight_MissingImageRefusesRatherThanFallingBack is the anti-silent-
// degradation assertion. A sandbox that runs the command on the host when the
// image is absent reports success while providing no boundary at all.
func TestPreflight_MissingImageRefusesRatherThanFallingBack(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, nil)
	fe.imageErr = errors.New("image not known")

	res, err := r.Run(context.Background(), Spec{Command: "echo", Args: []string{"hi"}})
	if err == nil {
		t.Fatal("a missing image must be an error, never a host-execution fallback")
	}
	if !errors.Is(err, ErrImageMissing) {
		t.Fatalf("error must be ErrImageMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "podman pull") {
		t.Fatalf("the error must tell the operator what to do; got %q", err)
	}
	if res.Outcome != OutcomeError {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeError)
	}
	// And crucially: no `run` was ever attempted.
	for _, call := range fe.calls {
		if len(call) > 0 && call[0] == "run" {
			t.Fatal("no container run may be attempted when the image is missing")
		}
	}
}

func TestPreflight_MissingEngine(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, nil)
	fe.lookErr = exec.ErrNotFound

	_, err := r.Run(context.Background(), Spec{Command: "echo"})
	if !errors.Is(err, ErrEngineMissing) {
		t.Fatalf("error must be ErrEngineMissing, got %v", err)
	}
	if !strings.Contains(err.Error(), "allow_unsandboxed_host_execution") {
		t.Fatalf("the error must name the explicit opt-out; got %q", err)
	}
}

func TestRun_TableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		spec        Spec
		runFn       func(ctx context.Context, args []string, out io.Writer) (int, error)
		wantOutcome Outcome
		wantExit    int
		wantErr     string
		wantPass    bool
	}{
		{
			name: "clean exit passes",
			spec: Spec{Command: "echo", Args: []string{"ok"}},
			runFn: func(_ context.Context, _ []string, out io.Writer) (int, error) {
				_, _ = out.Write([]byte("ok"))
				return 0, nil
			},
			wantOutcome: OutcomePassed,
			wantPass:    true,
		},
		{
			name: "non-zero exit is failed, not an error",
			spec: Spec{Command: "grep", Args: []string{"nothing"}},
			runFn: func(_ context.Context, _ []string, out io.Writer) (int, error) {
				_, _ = out.Write([]byte("no match"))
				return 1, errors.New("exit status 1")
			},
			wantOutcome: OutcomeFailed,
			wantExit:    1,
		},
		{
			name:    "command not on the allow-list",
			spec:    Spec{Command: "git", Args: []string{"log"}},
			wantErr: "not allow-listed",
		},
		{
			name:    "allow-list bypass still validates argv",
			spec:    Spec{Command: "find", Args: []string{".", "-exec", "id", ";"}, SkipAllowList: true},
			wantErr: "not permitted",
		},
		{
			name:    "path-shaped command name is rejected",
			spec:    Spec{Command: "/bin/sh", SkipAllowList: true},
			wantErr: "bare binary name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, fe := newTestRunner(t, nil)
			fe.runFn = tt.runFn
			res, err := r.Run(context.Background(), tt.spec)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Run() = %v, want an error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if res.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q, want %q", res.Outcome, tt.wantOutcome)
			}
			if res.ExitCode != tt.wantExit {
				t.Fatalf("exit code = %d, want %d", res.ExitCode, tt.wantExit)
			}
			if res.Pass() != tt.wantPass {
				t.Fatalf("Pass() = %v, want %v", res.Pass(), tt.wantPass)
			}
		})
	}
}

// TestRun_TimeoutIsDistinctFromFailure: a suite that never finished is not a
// suite that went red, and the verifier reports the difference.
func TestRun_TimeoutIsDistinctFromFailure(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, func(c Config) Config { c.Timeout = 50 * time.Millisecond; return c })
	fe.runFn = func(ctx context.Context, _ []string, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	res, err := r.Run(context.Background(), Spec{Command: "echo"})
	if err != nil {
		t.Fatalf("a timeout is a Result, not an error: %v", err)
	}
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeTimeout)
	}
	if res.Pass() {
		t.Fatal("a timed-out run must not report Pass()")
	}
}

// TestRun_SpecTimeoutCanOnlyShorten proves a caller cannot extend the
// configured ceiling by asking for a longer per-command timeout.
func TestRun_SpecTimeoutCanOnlyShorten(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, func(c Config) Config { c.Timeout = 60 * time.Millisecond; return c })
	var deadlineIn time.Duration
	fe.runFn = func(ctx context.Context, _ []string, _ io.Writer) (int, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			return 0, errors.New("expected a deadline")
		}
		deadlineIn = time.Until(dl)
		return 0, nil
	}
	if _, err := r.Run(context.Background(), Spec{Command: "echo", Timeout: time.Hour}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if deadlineIn > 2*time.Second {
		t.Fatalf("spec timeout extended the configured ceiling: deadline in %s", deadlineIn)
	}
}

func TestRun_OutputIsBoundedAndTruncationReported(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, func(c Config) Config { c.MaxOutputBytes = 128; return c })
	fe.runFn = func(_ context.Context, _ []string, out io.Writer) (int, error) {
		for i := 0; i < 100; i++ {
			if _, err := out.Write([]byte(strings.Repeat("y", 1024))); err != nil {
				return 1, fmt.Errorf("child got a write error, which becomes EPIPE: %w", err)
			}
		}
		return 0, nil
	}
	res, err := r.Run(context.Background(), Spec{Command: "echo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Output) != 128 {
		t.Fatalf("retained %d bytes, want the 128-byte cap", len(res.Output))
	}
	if !res.Truncated {
		t.Fatal("Truncated must be reported so the model knows the excerpt is partial")
	}
	if res.OutputSize != 100*1024 {
		t.Fatalf("OutputSize = %d, want %d", res.OutputSize, 100*1024)
	}
}

func TestNewRunner_DisabledYieldsNilRunner(t *testing.T) {
	t.Parallel()
	r, err := NewRunner(Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if r != nil {
		t.Fatal("a disabled sandbox must yield a nil runner so the caller has to decide explicitly")
	}
}

func TestNewRunner_InvalidConfigIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := NewRunner(Config{Enabled: true, Engine: "docker", Workspace: t.TempDir()}); err == nil {
		t.Fatal("docker must be refused at construction")
	}
}
