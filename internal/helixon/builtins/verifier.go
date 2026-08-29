package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

// VerifierCheck is one allow-listed way for the agent to prove its work.
//
// A check is a FIXED command with a fixed argument prefix. The agent chooses
// which check to run, never what to run: "verifier_run" is not a second shell
// wearing a hat. Extra arguments are permitted only where a check declares
// them, may not begin with "-" (which is how a benign-looking pattern becomes
// `-toolexec=...`), and where PathArgs is set must resolve inside the
// workspace mount.
type VerifierCheck struct {
	Name           string
	Command        string
	Args           []string
	AllowExtraArgs bool
	PathArgs       bool
	MinExtraArgs   int
	Description    string
}

// DefaultVerifierChecks is the check table the agent gets unless an operator
// narrows it.
func DefaultVerifierChecks() []VerifierCheck {
	return []VerifierCheck{
		{Name: "go_build", Command: "go", Args: []string{"build", "./..."}, AllowExtraArgs: true, Description: "compile every package"},
		{Name: "go_test", Command: "go", Args: []string{"test", "./..."}, AllowExtraArgs: true, Description: "run the test suite"},
		{Name: "go_vet", Command: "go", Args: []string{"vet", "./..."}, AllowExtraArgs: true, Description: "run go vet"},
		{Name: "gofmt_check", Command: "gofmt", Args: []string{"-l", "."}, Description: "list unformatted files (empty output means formatted)"},
		{Name: "file_exists", Command: "test", Args: []string{"-e"}, AllowExtraArgs: true, PathArgs: true, MinExtraArgs: 1, Description: "assert a workspace file exists"},
		{Name: "file_contains", Command: "grep", Args: []string{"-q", "--"}, AllowExtraArgs: true, MinExtraArgs: 2, Description: "assert a workspace file contains a pattern (args: pattern, path)"},
	}
}

// VerifierConfig configures the verifier_run tool.
type VerifierConfig struct {
	// Runner is required: the verifier exists to run commands under
	// containment, so it has no host-execution path at all.
	Runner *sandbox.Runner
	// Checks defaults to DefaultVerifierChecks.
	Checks []VerifierCheck
	// Timeout is the per-check ceiling. It can only shorten the sandbox's
	// own ceiling.
	Timeout time.Duration
	// MaxOutputBytes bounds the excerpt returned to the model, separately
	// from the sandbox's own retention cap.
	MaxOutputBytes int
	// OnOutcome, when set, is called once per invocation with the verdict.
	// It is how the verifier's pass/fail/error split reaches an operator
	// without this package depending on an instrumentation library.
	OnOutcome func(outcome string)
}

// Verifier outcomes reported to VerifierConfig.OnOutcome.
//
// `fail` is a check that ran and came back red. `error` is a check that never
// produced a verdict at all — an unknown check name, a malformed call, or a
// sandbox that could not start. They are deliberately separate: a verifier that
// cannot run is an outage, a verifier that fails is the gate doing its job, and
// an alert that cannot tell them apart is an alert nobody can act on.
const (
	VerifierOutcomePass  = "pass"
	VerifierOutcomeFail  = "fail"
	VerifierOutcomeError = "error"
)

// VerifierOutcomes returns every outcome this package emits, so the consuming
// side can assert it accepts all of them.
func VerifierOutcomes() []string {
	return []string{VerifierOutcomePass, VerifierOutcomeFail, VerifierOutcomeError}
}

func (c VerifierConfig) withDefaults() VerifierConfig {
	if len(c.Checks) == 0 {
		c.Checks = DefaultVerifierChecks()
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Minute
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 8 * 1024
	}
	return c
}

// VerifierResult is the structured payload the agent (and the completion
// gate) reads back. Outcome distinguishes a timeout from a non-zero exit:
// "the tests failed" and "the tests never finished" are different facts and
// collapsing them would let a hung suite masquerade as a red suite.
type VerifierResult struct {
	Check      string `json:"check"`
	Command    string `json:"command"`
	Pass       bool   `json:"pass"`
	Outcome    string `json:"outcome"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated"`
	OutputSize int    `json:"output_bytes"`
	Output     string `json:"output_excerpt"`
}

// VerifierToolName is the tool name the completion gate looks for.
const VerifierToolName = "verifier_run"

// VerifierTool returns the allow-listed, sandboxed verifier tool. It returns
// an error only when the call is malformed or the sandbox cannot run at all;
// a failing check comes back as a successful tool call carrying pass:false,
// because the agent needs to READ the failure in order to act on it.
func VerifierTool(cfg VerifierConfig) tooldispatch.ToolDef {
	cfg = cfg.withDefaults()
	checks := make(map[string]VerifierCheck, len(cfg.Checks))
	names := make([]string, 0, len(cfg.Checks))
	for _, c := range cfg.Checks {
		checks[c.Name] = c
		names = append(names, c.Name)
	}
	sort.Strings(names)

	return tooldispatch.ToolDef{
		Name: VerifierToolName,
		Description: "Run an allow-listed verification check inside the sandbox to PROVE the work is correct. " +
			"Available checks: " + strings.Join(names, ", ") + ". " +
			"Returns JSON: pass, outcome (passed|failed|timeout|error), exit_code, duration_ms, output_excerpt.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["check"],
			"properties": {
				"check": {"type": "string", "description": "Name of the allow-listed check to run."},
				"args":  {"type": "array", "description": "Extra arguments, where the check permits them."}
			}
		}`),
		Timeout: cfg.Timeout + 30*time.Second,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// Reported on every early return as well as on the verdict: a
			// verifier that is never reachable must be as visible as one that
			// keeps failing, and an observer that only fires on the happy path
			// would make an unreachable verifier look like no verifier calls.
			report := func(outcome string) {
				if cfg.OnOutcome != nil {
					cfg.OnOutcome(outcome)
				}
			}
			if cfg.Runner == nil {
				report(VerifierOutcomeError)
				return "", errors.New("verifier_run: no sandbox is configured; the verifier never executes on the host")
			}
			name, _ := args["check"].(string)
			check, ok := checks[name]
			if !ok {
				report(VerifierOutcomeError)
				return "", fmt.Errorf("verifier_run: unknown check %q (available: %s)", name, strings.Join(names, ", "))
			}
			extra, err := verifierExtraArgs(args)
			if err != nil {
				report(VerifierOutcomeError)
				return "", err
			}
			argv, err := buildVerifierArgv(check, extra, cfg.Runner.Config().WorkspaceMount)
			if err != nil {
				report(VerifierOutcomeError)
				return "", err
			}
			res, err := cfg.Runner.Run(ctx, sandbox.Spec{
				Command:       check.Command,
				Args:          argv,
				Timeout:       cfg.Timeout,
				SkipAllowList: true,
			})
			if err != nil {
				report(VerifierOutcomeError)
				return "", fmt.Errorf("verifier_run %s: %w", name, err)
			}
			if res.Pass() {
				report(VerifierOutcomePass)
			} else {
				report(VerifierOutcomeFail)
			}
			return encodeVerifierResult(name, res, cfg.MaxOutputBytes)
		},
	}
}

// encodeVerifierResult turns a sandbox result into the JSON verdict the agent
// and the completion gate read. It is a separate function so the mapping —
// which is what the gate's correctness depends on — can be tested without
// starting a container.
//
//nolint:gocritic // hugeParam: sandbox.Result is a value type by design
func encodeVerifierResult(check string, res sandbox.Result, maxOutputBytes int) (string, error) {
	out := VerifierResult{
		Check:      check,
		Command:    res.Command,
		Pass:       res.Pass(),
		Outcome:    string(res.Outcome),
		ExitCode:   res.ExitCode,
		DurationMS: res.DurationMS,
		Truncated:  res.Truncated,
		OutputSize: res.OutputSize,
		Output:     truncateBytes(res.Output, maxOutputBytes),
	}
	// Truncating for the model is itself a truncation, even when the sandbox
	// retained everything it saw.
	if len(out.Output) < len(res.Output) {
		out.Truncated = true
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("verifier_run: encode result: %w", err)
	}
	return string(data), nil
}

// verifierExtraArgs decodes the optional args array.
func verifierExtraArgs(args map[string]any) ([]string, error) {
	raw, ok := args["args"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("verifier_run: args must be an array, got %T", raw)
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("verifier_run: args entry %d must be a string, got %T", i, item)
		}
		out = append(out, s)
	}
	return out, nil
}

// buildVerifierArgv validates the caller's extra arguments and returns the
// full argument vector for the check's fixed command. The check is taken by
// value so a handler cannot mutate the shared check table under a concurrent
// call.
//
//nolint:gocritic // hugeParam: value semantics are deliberate, see above
func buildVerifierArgv(check VerifierCheck, extra []string, workspaceMount string) ([]string, error) {
	if len(extra) > 0 && !check.AllowExtraArgs {
		return nil, fmt.Errorf("verifier_run: check %q does not accept extra arguments", check.Name)
	}
	if len(extra) < check.MinExtraArgs {
		return nil, fmt.Errorf("verifier_run: check %q requires at least %d argument(s), got %d", check.Name, check.MinExtraArgs, len(extra))
	}
	for i, a := range extra {
		if strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf("verifier_run: check %q: argument %d may not be a flag (%q)", check.Name, i, a)
		}
	}
	argv := append([]string(nil), check.Args...)
	if check.PathArgs {
		for i, a := range extra {
			canon, err := sandbox.Contains(workspaceMount, a)
			if err != nil {
				return nil, fmt.Errorf("verifier_run: check %q: argument %d: %w", check.Name, i, err)
			}
			extra[i] = canon
		}
	}
	argv = append(argv, extra...)
	if err := sandbox.ValidateArgv(check.Command, argv); err != nil {
		return nil, fmt.Errorf("verifier_run: check %q: %w", check.Name, err)
	}
	return argv, nil
}
