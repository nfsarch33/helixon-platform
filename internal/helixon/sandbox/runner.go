package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Outcome distinguishes the ways a sandboxed command can end. "timeout" is
// deliberately NOT folded into "failed": a run that was killed at the wall
// clock says nothing about the code under test, while a non-zero exit says
// something quite specific, and the completion gate treats them differently.
type Outcome string

// Command outcomes.
const (
	OutcomePassed  Outcome = "passed"
	OutcomeFailed  Outcome = "failed"
	OutcomeTimeout Outcome = "timeout"
	OutcomeError   Outcome = "error"
)

// ErrImageMissing is returned when the configured image is not present.
// It is deliberately fatal to the run: see ErrHostExecutionRefused.
var ErrImageMissing = errors.New("sandbox: execution image is not present")

// ErrEngineMissing is returned when the container engine binary is absent.
var ErrEngineMissing = errors.New("sandbox: container engine is not available")

// Spec is one command to run inside the sandbox.
type Spec struct {
	Command string
	Args    []string
	// WorkDir is an optional in-container working directory. It defaults to
	// the workspace mount.
	WorkDir string
	// Timeout overrides Config.Timeout for this command; it can only
	// shorten it, never extend it past the configured ceiling.
	Timeout time.Duration
	// SkipAllowList runs a command that the caller has already vetted
	// against its own narrower allow-list (the verifier's fixed check
	// table). It never skips ValidateArgv.
	SkipAllowList bool
}

// Result is the structured outcome of one sandboxed command.
type Result struct {
	Command    string  `json:"command"`
	Outcome    Outcome `json:"outcome"`
	ExitCode   int     `json:"exit_code"`
	Output     string  `json:"output_excerpt"`
	Truncated  bool    `json:"truncated"`
	OutputSize int     `json:"output_bytes"`
	DurationMS int64   `json:"duration_ms"`
}

// Pass reports whether the command completed with exit status zero. Result is
// a value type throughout: a pointer receiver here would invite callers to
// hold a reference to a result something else can still change.
//
//nolint:gocritic // hugeParam: value semantics are deliberate, see above
func (r Result) Pass() bool { return r.Outcome == OutcomePassed }

// engine abstracts process execution so the argv construction and the
// error mapping can be tested without a container runtime.
type engine interface {
	run(ctx context.Context, name string, args []string, out io.Writer) (int, error)
	lookPath(name string) error
}

type osEngine struct{}

func (osEngine) lookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
}

func (osEngine) run(ctx context.Context, name string, args []string, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204 argv is built from a validated allow-list; name is the configured engine
	cmd.Stdout = out
	cmd.Stderr = out
	// The container is killed by CommandContext when ctx expires; WaitDelay
	// bounds how long we wait afterwards for the io copy to drain so a
	// wedged child cannot hold the goroutine forever.
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), err
	}
	return -1, err
}

// Runner executes commands inside a hardened container.
type Runner struct {
	cfg    Config
	engine engine
}

// NewRunner validates cfg and returns a Runner. A disabled config returns
// (nil, nil): callers treat a nil Runner as "no sandbox configured" and must
// decide explicitly what to do about it.
//
//nolint:gocritic // hugeParam: see Config.Normalize on the value semantics
func NewRunner(cfg Config) (*Runner, error) {
	if !cfg.Enabled {
		return nil, nil //nolint:nilnil // a disabled sandbox is a nil runner, not an error
	}
	cfg = cfg.Normalize(WorkingDir())
	validated, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: validated, engine: osEngine{}}, nil
}

// Config returns the validated configuration by value, so a caller cannot
// mutate a live runner's configuration through a pointer.
//
//nolint:gocritic // hugeParam: returning by value is the point, see above
func (r *Runner) Config() Config { return r.cfg }

// EngineArgs builds the full engine argument vector for spec. It is exported
// so the hardening flags can be asserted without a container runtime — every
// flag here is a security control and each one has a test that fails when it
// is removed.
func (r *Runner) EngineArgs(spec Spec) []string {
	args := []string{
		"run", "--rm",
		"--network=" + r.cfg.Network,
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--user=" + r.cfg.User,
		"--memory=" + r.cfg.MemoryLimit,
		"--pids-limit=" + strconv.Itoa(r.cfg.PidsLimit),
		"--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=" + r.cfg.TmpfsSize,
	}
	if r.cfg.WorkspaceAccess != WorkspaceNone {
		mode := "ro"
		if r.cfg.WorkspaceAccess == WorkspaceRW {
			mode = "rw"
		}
		args = append(args, "--volume="+r.cfg.Workspace+":"+r.cfg.WorkspaceMount+":"+mode)
	}
	for _, b := range r.cfg.Binds {
		mode := "ro"
		if b.ReadWrite {
			mode = "rw"
		}
		args = append(args, "--volume="+b.Host+":"+b.Container+":"+mode)
	}
	// The host environment is never forwarded; only explicitly configured
	// variables cross the boundary, in a stable order.
	keys := make([]string, 0, len(r.cfg.Env))
	for k := range r.cfg.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env="+k+"="+r.cfg.Env[k])
	}
	workdir := r.cfg.WorkspaceMount
	if spec.WorkDir != "" {
		workdir = spec.WorkDir
	}
	args = append(args, "--workdir="+workdir, r.cfg.Image, spec.Command)
	return append(args, spec.Args...)
}

// Preflight verifies that the engine binary and the pinned image are both
// present. A missing image is an error, never a fallback: the caller asked
// for containment, and running the command on the host instead would give
// them the opposite of what they asked for while reporting success.
func (r *Runner) Preflight(ctx context.Context) error {
	if err := r.engine.lookPath(r.cfg.Engine); err != nil {
		return fmt.Errorf("%w: %q not found on PATH — install podman or set sandbox.enabled: false and accept host execution explicitly via sandbox.allow_unsandboxed_host_execution: %w",
			ErrEngineMissing, r.cfg.Engine, err)
	}
	out := NewBoundedBuffer(4096)
	code, err := r.engine.run(ctx, r.cfg.Engine, []string{"image", "exists", r.cfg.Image}, out)
	if err == nil && code == 0 {
		return nil
	}
	if code < 0 {
		return fmt.Errorf("%w: could not query %s for image %q: %w", ErrEngineMissing, r.cfg.Engine, r.cfg.Image, err)
	}
	return fmt.Errorf("%w: %q — run `%s pull %s` on this host, or set sandbox.image to an image that is present. Refusing to fall back to host execution",
		ErrImageMissing, r.cfg.Image, r.cfg.Engine, r.cfg.Image)
}

// Run executes spec inside the sandbox and returns a bounded, structured
// result. A non-zero exit is reported as a Result, not an error; only a
// failure to run the sandbox at all (missing engine, missing image, rejected
// argv) is returned as an error.
func (r *Runner) Run(ctx context.Context, spec Spec) (Result, error) {
	if err := ValidateArgv(spec.Command, spec.Args); err != nil {
		return Result{Outcome: OutcomeError, ExitCode: -1}, err
	}
	if !spec.SkipAllowList {
		if err := r.cfg.CheckAllowed(spec.Command); err != nil {
			return Result{Outcome: OutcomeError, ExitCode: -1}, err
		}
	}
	if err := r.Preflight(ctx); err != nil {
		return Result{Outcome: OutcomeError, ExitCode: -1}, err
	}

	timeout := r.cfg.Timeout
	if spec.Timeout > 0 && spec.Timeout < timeout {
		timeout = spec.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := NewBoundedBuffer(r.cfg.MaxOutputBytes)
	started := time.Now()
	code, err := r.engine.run(runCtx, r.cfg.Engine, r.EngineArgs(spec), out)
	elapsed := time.Since(started)

	res := Result{
		Command:    strings.TrimSpace(spec.Command + " " + strings.Join(spec.Args, " ")),
		ExitCode:   code,
		Output:     out.String(),
		Truncated:  out.Truncated(),
		OutputSize: out.Total(),
		DurationMS: elapsed.Milliseconds(),
	}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.Outcome = OutcomeTimeout
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
	case err == nil && code == 0:
		res.Outcome = OutcomePassed
	case code > 0:
		res.Outcome = OutcomeFailed
	default:
		res.Outcome = OutcomeError
		return res, fmt.Errorf("sandbox: %s run: %w", r.cfg.Engine, err)
	}
	return res, nil
}
