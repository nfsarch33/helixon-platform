package sandbox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

// ContainerLabel is stamped on every container this package starts.
//
// `--rm` removes a container that EXITS. It does nothing for a container whose
// client was killed, and that is the common case here: a run killed at its
// deadline kills the engine CLI, while the container it started keeps running
// unsupervised — holding its workspace bind mount, its share of the host, and
// a slot in the engine's state database. Run reaps by name, but a test binary
// that panics or is killed outright never gets to run any cleanup at all, so
// the label is the out-of-band handle for whatever is left:
//
//	podman rm -f $(podman ps -aq --filter label=hlxn.sandbox=1)
//
// Measured on win1/wsl1 before this existed: 28 containers from earlier
// timed-out runs were still up, the oldest for seven hours, and the engine had
// degraded to the point where `podman ps -a` did not return within 229s.
const ContainerLabel = "hlxn.sandbox=1"

// reapTimeout bounds the best-effort removal of a container left behind by a
// killed run. It is a ceiling, not a promise: on a host whose engine is
// already struggling the removal is exactly what will be slow, and blocking a
// caller indefinitely to tidy up would convert a bounded timeout into an
// unbounded one. Whatever it fails to remove keeps ContainerLabel.
const reapTimeout = 60 * time.Second

// Failure kinds reported to a FailureObserver.
//
// The split is the one the operator has to act on, not the one the code finds
// convenient — and an earlier version of this comment got that split wrong. It
// filed a rejected argv and a missing container image under one `preflight`
// kind, on the reasoning that both stop the command before the container starts
// and so are "one operator action". Live traffic disproved it. On 2026-08-29 all
// three observed increments were the model emitting shell pipelines
// (`ls -la /workspace/ 2>&1 | head -50`) which ValidateArgv correctly refused.
// That paged an operator to investigate a broken sandbox that was, in fact,
// working exactly as designed. The two cases need opposite responses, so they
// need different labels:
//
//   - `rejected` means the sandbox refused the command the agent asked for — a
//     path instead of a bare binary, or a command outside the allow-list. This
//     is CONTAINMENT WORKING. Nothing is broken on the host and no operator
//     action fixes it; a sustained rate is a signal about the model's prompt
//     contract, not an outage.
//   - `preflight` means the sandbox could not start ANY command: engine binary
//     missing, image missing, workspace scratch unusable. This is a genuine
//     host or config problem and the agent can do no work until it is fixed.
//   - `timeout` means the command ran and was killed at the wall clock, which
//     says nothing about the code under test.
//   - `exec` means the engine itself failed.
//
// A non-zero exit is NOT here: a red check is a verdict, not a sandbox failure,
// and counting it as one would bury the failures that matter under ordinary red
// builds. `rejected` was split out for the same reason — it was burying the
// outage signal under the boundary doing its job.
const (
	FailureKindRejected  = "rejected"
	FailureKindPreflight = "preflight"
	FailureKindTimeout   = "timeout"
	FailureKindExec      = "exec"
)

// FailureKinds returns every kind this package emits. Exported so the metrics
// side can assert it accepts all of them, rather than each side testing its own
// copy of the strings and both passing while disagreeing.
func FailureKinds() []string {
	return []string{FailureKindRejected, FailureKindPreflight, FailureKindTimeout, FailureKindExec}
}

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
	// onFailure is an optional observer for runs that never produced a
	// verdict. It is a plain callback rather than a metrics handle so this
	// package keeps no dependency on an instrumentation library: the sandbox
	// is the most security-sensitive code here and its import list should
	// stay short enough to read.
	onFailure func(kind string)
}

// SetFailureObserver installs fn, called once per run that failed to produce a
// verdict. It must be called before the runner is used; the callback is read
// without synchronization on every Run, which is safe only because wiring
// happens once at start-up and never again.
func (r *Runner) SetFailureObserver(fn func(kind string)) {
	r.onFailure = fn
}

func (r *Runner) reportFailure(kind string) {
	if r.onFailure != nil {
		r.onFailure(kind)
	}
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

// EngineArgs builds the full engine argument vector for spec, for a container
// registered under name. It is exported so the hardening flags can be asserted
// without a container runtime — every flag here is a security control and each
// one has a test that fails when it is removed.
//
// name is a parameter rather than something generated in here so that this
// stays a pure function of its inputs: the argv the tests assert is then the
// argv that actually runs, instead of one that differs from it by the field
// cleanup depends on.
func (r *Runner) EngineArgs(spec Spec, name string) []string {
	args := []string{
		"run", "--rm",
		"--name=" + name,
		"--label=" + ContainerLabel,
		"--network=" + r.cfg.Network,
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
	}
	// keep-id and --user are alternatives, never both: podman under keep-id
	// already runs the container as the invoking (non-root) user, and pinning
	// an unrelated uid on top is what left the sandbox unable to write its own
	// bind-mounted workspace. Config.Validate rejects a config that asks for
	// both, so this branch never has to guess.
	if r.cfg.UserNS == UserNSKeepID {
		args = append(args, "--userns="+UserNSKeepID)
	} else {
		args = append(args, "--user="+r.cfg.User)
	}
	args = append(args,
		"--memory="+r.cfg.MemoryLimit,
		"--pids-limit="+strconv.Itoa(r.cfg.PidsLimit),
		"--tmpfs=/tmp:rw,noexec,nosuid,nodev,size="+r.cfg.TmpfsSize,
	)
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

// EnsureWorkspaceScratch creates the host directory that backs an
// in-container GOTMPDIR living inside the workspace mount.
//
// The Go toolchain does NOT create GOTMPDIR; it calls MkdirTemp inside it and
// fails if the parent is missing. A freshly cloned or freshly created
// workspace has no .gotmp, so without this every first run in a new workspace
// would fail — which is the same class of "hardened into uselessness" bug this
// change exists to remove, just deferred to the first run instead of every
// run.
//
// It is deliberately driven by the EFFECTIVE environment rather than by a
// constant: an operator who redirects GOTMPDIR through sandbox.env gets no
// surprise directory created in their workspace, and an operator who redirects
// it to a different workspace-relative path still gets it created.
//
// A GOTMPDIR outside the workspace mount is left alone: it belongs to whatever
// bind mount or tmpfs the operator pointed it at, and creating paths on the
// host outside the declared workspace is precisely what this package refuses
// to do elsewhere.
func (r *Runner) EnsureWorkspaceScratch() error {
	if r.cfg.WorkspaceAccess != WorkspaceRW {
		return nil
	}
	target := r.cfg.Env["GOTMPDIR"]
	if target == "" {
		return nil
	}
	rel, ok := containerPathWithin(r.cfg.WorkspaceMount, target)
	if !ok {
		return nil
	}
	hostPath := filepath.Join(r.cfg.Workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(hostPath, 0o750); err != nil {
		return fmt.Errorf("sandbox: create workspace scratch directory %q for GOTMPDIR=%q: %w", hostPath, target, err)
	}
	return nil
}

// containerPathWithin reports whether target (an in-container, always
// slash-separated path) lives at or under mount, and returns its path relative
// to mount. Cleaning first is what makes "/workspace/../etc" answer false
// instead of passing a prefix test.
func containerPathWithin(mount, target string) (string, bool) {
	mount = path.Clean(mount)
	target = path.Clean(target)
	if target == mount {
		return ".", true
	}
	rel, found := strings.CutPrefix(target, mount+"/")
	if !found || rel == "" {
		return "", false
	}
	return rel, true
}

// containerName returns a unique name for one container. Uniqueness is what
// makes reaping safe: a fixed name would let a reap for one run remove the
// container of another that happened to be in flight beside it.
func containerName() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Only reached if the kernel CSPRNG is unavailable. A collision
		// costs one refused run — the engine rejects a duplicate name
		// loudly — which is strictly better than refusing to run at all.
		return fmt.Sprintf("hlxn-sbx-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return fmt.Sprintf("hlxn-sbx-%d-%x", os.Getpid(), b[:])
}

// reap force-removes a container the engine may have left running. It is
// deliberately best-effort: the run it belongs to has already produced its
// verdict, and a cleanup failure must not change that verdict into an error.
//
// The fresh context is load-bearing. The run's context is precisely the one
// that just expired, and a cleanup that inherited it would be canceled before
// it issued a single call — which is the shape this bug had in the first place.
//
//nolint:contextcheck // detaching from the run's context is the entire point; see above
func (r *Runner) reap(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), reapTimeout)
	defer cancel()
	_, _ = r.engine.run(ctx, r.cfg.Engine, []string{"rm", "--force", "--ignore", name}, io.Discard)
}

// Run executes spec inside the sandbox and returns a bounded, structured
// result. A non-zero exit is reported as a Result, not an error; only a
// failure to run the sandbox at all (missing engine, missing image, rejected
// argv) is returned as an error.
func (r *Runner) Run(ctx context.Context, spec Spec) (Result, error) {
	// Both of these are the boundary refusing what the agent asked for, which is
	// the boundary working. They must not share a label with "the sandbox cannot
	// start at all" — see the FailureKind* comment.
	if err := ValidateArgv(spec.Command, spec.Args); err != nil {
		r.reportFailure(FailureKindRejected)
		return Result{Outcome: OutcomeError, ExitCode: -1}, err
	}
	if !spec.SkipAllowList {
		if err := r.cfg.CheckAllowed(spec.Command); err != nil {
			r.reportFailure(FailureKindRejected)
			return Result{Outcome: OutcomeError, ExitCode: -1}, err
		}
	}
	if err := r.Preflight(ctx); err != nil {
		r.reportFailure(FailureKindPreflight)
		return Result{Outcome: OutcomeError, ExitCode: -1}, err
	}
	if err := r.EnsureWorkspaceScratch(); err != nil {
		r.reportFailure(FailureKindPreflight)
		return Result{Outcome: OutcomeError, ExitCode: -1}, err
	}

	timeout := r.cfg.Timeout
	if spec.Timeout > 0 && spec.Timeout < timeout {
		timeout = spec.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := containerName()
	out := NewBoundedBuffer(r.cfg.MaxOutputBytes)
	started := time.Now()
	code, err := r.engine.run(runCtx, r.cfg.Engine, r.EngineArgs(spec, name), out)
	elapsed := time.Since(started)
	// A run that ended on its own was already removed by `--rm`. A run that
	// was CANCELED had its engine client killed instead, which leaves the
	// container behind: reap it explicitly, or every timeout permanently
	// costs the host one running container.
	if runCtx.Err() != nil {
		r.reap(name)
	}

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
		r.reportFailure(FailureKindTimeout)
	case err == nil && code == 0:
		res.Outcome = OutcomePassed
	case code > 0:
		res.Outcome = OutcomeFailed
	default:
		res.Outcome = OutcomeError
		r.reportFailure(FailureKindExec)
		return res, fmt.Errorf("sandbox: %s run: %w", r.cfg.Engine, err)
	}
	return res, nil
}
