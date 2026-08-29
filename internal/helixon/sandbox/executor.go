package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// Disposition is what the sandbox does with a tool call. The values are
// ordered from least to most restrictive; that ordering is what makes tool
// policy layering monotonic (see Policy.Layer).
type Disposition int

// Dispositions, least restrictive first.
const (
	// DispositionAllow forwards the call to the inner executor unchanged.
	DispositionAllow Disposition = iota
	// DispositionPathGuard canonicalizes every path-shaped argument and
	// rejects the call if any of them resolves outside the workspace,
	// then forwards it.
	DispositionPathGuard
	// DispositionSandbox intercepts the call: the {command, args} payload
	// runs inside the container and the inner handler is never reached.
	DispositionSandbox
	// DispositionDeny refuses the call.
	DispositionDeny
)

func (d Disposition) String() string {
	switch d {
	case DispositionAllow:
		return "allow"
	case DispositionPathGuard:
		return "path_guard"
	case DispositionSandbox:
		return "sandbox"
	case DispositionDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// ErrToolDenied is returned when policy denies a tool outright.
var ErrToolDenied = errors.New("sandbox: tool denied by policy")

// ErrHostExecutionRefused is returned when a command-executing tool is
// reached with no sandbox behind it. It exists so the failure is loud: the
// alternative — running the command on the host — would report success while
// delivering the exact opposite of the requested guarantee.
var ErrHostExecutionRefused = errors.New("sandbox: refusing to execute on the host without a sandbox")

// Policy maps tool names to dispositions.
type Policy struct {
	// Default applies to any tool not named in Tools.
	Default Disposition
	Tools   map[string]Disposition
}

// DefaultPolicy classifies the built-in tools.
//
// The default disposition is Deny. It used to be PathGuard, on the theory
// that a new read-only tool should not silently break an agent — but the
// theory had the failure mode backwards. PathGuard forwards a call to the
// HOST handler after canonicalizing its path arguments, so every tool nobody
// had classified ran on the host, with the host's network and the host's
// ambient authority, while the sandbox advertised --network=none. web_fetch
// was the concrete case: a tool whose entire purpose is outbound network
// access, unlisted, therefore host-executed, therefore a live exfiltration
// path out of a container that was configured to have no network at all.
//
// Unlisted now means denied, and every tool this repository registers is
// named below. The cost of that is real and deliberate: adding a tool without
// classifying it here makes it fail loudly on first use, rather than quietly
// running outside the boundary. A gap that fails closed is a bug report; a
// gap that fails open is an incident.
//
// The classifications:
//
//   - shell — Sandbox. It is the command-execution primitive; it is
//     intercepted and its {command, args} payload runs in the container.
//   - file_read / file_write — PathGuard. They take a path and touch the
//     filesystem; the guard canonicalizes the path against the workspace and
//     the host handler then operates on the approved value.
//   - verifier_run — Allow. It is sandboxed by construction: its handler owns
//     the same *Runner and cannot reach the host. Routing it through
//     DispositionSandbox as well would nest one container inside another.
//   - memory* / sprintboard* — Allow. They talk to the agent's own memory
//     store and its own control plane over a URL fixed by the operator's
//     config. There is no caller-controlled destination and no command
//     execution, so there is nothing for the guard or the container to add.
//   - web_fetch — Deny. See below.
//   - autoresearch_run — Deny. It shells out to a helper binary with
//     os.Environ() attached, which is a second unsandboxed execution
//     primitive wearing a different name.
//
// Why web_fetch is Deny rather than Sandbox: sandbox routing means "read the
// {command, args} payload and run it in the container". web_fetch has no such
// payload — it takes a url — so routing it there would only produce a
// confusing argument error. Nor could the container serve it: the sandbox
// runs --network=none, and Config.Validate already refuses to pair network
// access with a writable workspace. A tool that exists to reach the network
// cannot be honestly offered from behind a boundary whose defining property
// is that it has none. An operator who genuinely wants agent egress must run
// with the sandbox disabled and say so through
// allow_unsandboxed_host_execution.
func DefaultPolicy() Policy {
	return Policy{
		Default: DispositionDeny,
		Tools: map[string]Disposition{
			"shell":        DispositionSandbox,
			"file_read":    DispositionPathGuard,
			"file_write":   DispositionPathGuard,
			"verifier_run": DispositionAllow,

			"memory":                      DispositionAllow,
			"memory.read":                 DispositionAllow,
			"memory.search":               DispositionAllow,
			"memory.write":                DispositionAllow,
			"sprintboard":                 DispositionAllow,
			"sprintboard.claim_ticket":    DispositionAllow,
			"sprintboard.complete_ticket": DispositionAllow,

			"web_fetch":        DispositionDeny,
			"autoresearch_run": DispositionDeny,
		},
	}
}

// For returns the disposition for a tool.
func (p Policy) For(tool string) Disposition {
	if d, ok := p.Tools[tool]; ok {
		return d
	}
	return p.Default
}

// Layer composes next on top of p and can only RESTRICT. For every tool the
// result is the more restrictive of the two dispositions, and the default is
// likewise the more restrictive of the two — so no later layer, config file,
// or caller can re-grant something an earlier layer took away. That
// monotonicity is the property worth having: a policy stack you can only
// tighten cannot be widened by appending to it.
func (p Policy) Layer(next Policy) Policy {
	out := Policy{
		Default: maxDisposition(p.Default, next.Default),
		Tools:   make(map[string]Disposition, len(p.Tools)+len(next.Tools)),
	}
	for name := range p.Tools {
		out.Tools[name] = maxDisposition(p.For(name), next.For(name))
	}
	for name := range next.Tools {
		if _, done := out.Tools[name]; done {
			continue
		}
		out.Tools[name] = maxDisposition(p.For(name), next.For(name))
	}
	return out
}

// WithDefault raises the fallback disposition for unlisted tools. Like
// Layer, it can only restrict: asking for a looser default is a no-op, so
// `deny_unlisted_tools: false` cannot widen a policy that was already strict.
func (p Policy) WithDefault(d Disposition) Policy {
	out := Policy{Default: maxDisposition(p.Default, d), Tools: make(map[string]Disposition, len(p.Tools))}
	for name, disp := range p.Tools {
		out.Tools[name] = disp
	}
	return out
}

// PolicyFor returns the policy a config asks for.
//
// Since the default set became default-deny, DenyUnlistedTools no longer has
// anything left to tighten and this is effectively DefaultPolicy(). The call
// is kept because WithDefault is monotonic: a future looser default could
// still be raised here, and `deny_unlisted_tools: false` provably cannot
// widen the policy — asking for a looser default is a no-op by construction.
//
//nolint:gocritic // hugeParam: Config is passed by value throughout this package, see Config.Normalize
func PolicyFor(cfg Config) Policy {
	p := DefaultPolicy()
	if cfg.DenyUnlistedTools {
		p = p.WithDefault(DispositionDeny)
	}
	return p
}

func maxDisposition(a, b Disposition) Disposition {
	if a > b {
		return a
	}
	return b
}

// DefaultPathArgs are the argument names treated as filesystem paths.
var DefaultPathArgs = []string{"path", "file", "dir", "directory", "output_path", "source", "target"}

// Executor decorates an inner tool executor with the sandbox boundary. It
// satisfies agent.ToolExecutor, so it drops in wherever the registry does.
type Executor struct {
	inner    InnerExecutor
	runner   *Runner
	policy   Policy
	pathArgs map[string]struct{}
	logger   *slog.Logger
}

// InnerExecutor is the minimum surface Executor wraps (*tooldispatch.Registry
// and the other decorators all satisfy it).
type InnerExecutor interface {
	Execute(ctx context.Context, name, argsJSON string) (string, error)
	Available() []llm.Tool
}

// NewExecutor wraps inner. runner must be non-nil: an Executor with no
// sandbox behind it would be a boundary in name only.
func NewExecutor(inner InnerExecutor, runner *Runner, policy Policy, logger *slog.Logger) (*Executor, error) {
	if inner == nil {
		return nil, errors.New("sandbox: executor requires an inner executor")
	}
	if runner == nil {
		return nil, errors.New("sandbox: executor requires a runner")
	}
	if len(policy.Tools) == 0 && policy.Default == DispositionAllow {
		policy = DefaultPolicy()
	}
	if logger == nil {
		logger = slog.Default()
	}
	pathArgs := make(map[string]struct{}, len(DefaultPathArgs))
	for _, a := range DefaultPathArgs {
		pathArgs[a] = struct{}{}
	}
	return &Executor{
		inner:    inner,
		runner:   runner,
		policy:   policy,
		pathArgs: pathArgs,
		logger:   logger.With(slog.String("component", "helixon.sandbox")),
	}, nil
}

// Policy returns the effective policy.
func (e *Executor) Policy() Policy { return e.policy }

// Available proxies to the inner executor: the sandbox changes how tools run,
// not which tools the model is told about.
func (e *Executor) Available() []llm.Tool { return e.inner.Available() }

// Execute applies the policy for name and then either denies, sandboxes,
// path-guards, or forwards the call.
func (e *Executor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
	switch e.policy.For(name) {
	case DispositionDeny:
		return "", fmt.Errorf("%w: %q", ErrToolDenied, name)
	case DispositionSandbox:
		return e.executeSandboxed(ctx, name, argsJSON)
	case DispositionPathGuard:
		guarded, err := e.guardPaths(name, argsJSON)
		if err != nil {
			return "", err
		}
		return e.inner.Execute(ctx, name, guarded)
	case DispositionAllow:
		return e.inner.Execute(ctx, name, argsJSON)
	default:
		return "", fmt.Errorf("%w: %q has an unknown disposition", ErrToolDenied, name)
	}
}

// executeSandboxed reads the {command, args} payload and runs it in the
// container. The inner handler is never called, so a tool whose host-side
// implementation is unsafe becomes safe by being routed here.
func (e *Executor) executeSandboxed(ctx context.Context, name, argsJSON string) (string, error) {
	args, err := decodeArgs(argsJSON)
	if err != nil {
		return "", fmt.Errorf("sandbox: %s: %w", name, err)
	}
	command, _ := args["command"].(string)
	if command == "" {
		return "", fmt.Errorf("sandbox: %s: sandboxed tools require a string \"command\" argument", name)
	}
	argv, err := decodeStringArray(args["args"])
	if err != nil {
		return "", fmt.Errorf("sandbox: %s: %w", name, err)
	}
	res, err := e.runner.Run(ctx, Spec{Command: command, Args: argv})
	if err != nil {
		return res.Output, err
	}
	e.logger.Debug("sandboxed tool call",
		slog.String("tool", name),
		slog.String("command", command),
		slog.String("outcome", string(res.Outcome)),
		slog.Int("exit_code", res.ExitCode),
	)
	if res.Outcome != OutcomePassed {
		return res.Output, fmt.Errorf("sandbox: %s %s: %s (exit %d)", name, command, res.Outcome, res.ExitCode)
	}
	return res.Output, nil
}

// guardPaths canonicalizes every path-shaped argument against the workspace
// and rewrites the payload with the canonical values, so the inner handler
// operates on the path the guard actually approved rather than re-resolving
// the caller's string (which is how TOCTOU bugs get written).
func (e *Executor) guardPaths(name, argsJSON string) (string, error) {
	args, err := decodeArgs(argsJSON)
	if err != nil {
		return "", fmt.Errorf("sandbox: %s: %w", name, err)
	}
	root := e.runner.Config().Workspace
	changed := false
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if _, ok := e.pathArgs[k]; !ok {
			continue
		}
		raw, ok := args[k].(string)
		if !ok || raw == "" {
			continue
		}
		canon, cErr := Contains(root, raw)
		if cErr != nil {
			return "", fmt.Errorf("sandbox: %s: argument %q: %w", name, k, cErr)
		}
		args[k] = canon
		changed = true
	}
	if !changed {
		return argsJSON, nil
	}
	out, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("sandbox: %s: re-encode arguments: %w", name, err)
	}
	return string(out), nil
}

func decodeArgs(argsJSON string) (map[string]any, error) {
	args := map[string]any{}
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return nil, fmt.Errorf("decode arguments: %w", err)
	}
	return args, nil
}

func decodeStringArray(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("\"args\" must be an array, got %T", v)
	}
	out := make([]string, 0, len(raw))
	for i, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("\"args\" entry %d must be a string, got %T", i, item)
		}
		out = append(out, s)
	}
	return out, nil
}
