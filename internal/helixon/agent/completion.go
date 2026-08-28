package agent

import (
	"encoding/json"
	"errors"
)

var (
	// ErrNoVerifierEvidence is returned when a run that changed state tries
	// to finish without a passing verifier check.
	//
	// The loop's previous definition of success was "the model stopped
	// calling tools", which is a statement about the model's confidence and
	// not about the work. A run that edited files and then said "done" was
	// indistinguishable from a run that edited files, broke the build, and
	// said "done".
	ErrNoVerifierEvidence = errors.New("agent: completion refused: the run changed state but produced no passing verifier evidence")

	// ErrNeedsHumanApproval is returned when the verifier has failed
	// MaxConsecutiveFailures times in a row. Retrying past that point is
	// how an agent burns a token budget converging on nothing, so the run
	// stops and asks for a human instead.
	ErrNeedsHumanApproval = errors.New("agent: verifier failed repeatedly; stopping for human approval")
)

// CompletionPolicy governs when a run may be reported complete.
type CompletionPolicy struct {
	// Enabled turns the gate on. Even when enabled, the gate stays inert
	// unless a verifier tool is actually available to the run: demanding
	// evidence the agent has no way to produce would only convert working
	// runs into failures.
	Enabled bool
	// VerifierTool is the tool name whose result carries the evidence.
	VerifierTool string
	// MutatingTools are the tools whose use makes a run "code-shaped", i.e.
	// state-changing and therefore subject to the evidence requirement.
	MutatingTools []string
	// MaxConsecutiveFailures is the escalation threshold.
	MaxConsecutiveFailures int
}

// DefaultCompletionPolicy is the conservative default: the gate is on, two
// consecutive verifier failures escalate, and shell/file_write mark a run as
// state-changing.
func DefaultCompletionPolicy() CompletionPolicy {
	return CompletionPolicy{
		Enabled:                true,
		VerifierTool:           "verifier_run",
		MutatingTools:          []string{"shell", "file_write", "verifier_run"},
		MaxConsecutiveFailures: 2,
	}
}

func (p CompletionPolicy) withDefaults() CompletionPolicy {
	if p.VerifierTool == "" {
		p.VerifierTool = DefaultCompletionPolicy().VerifierTool
	}
	if len(p.MutatingTools) == 0 {
		p.MutatingTools = DefaultCompletionPolicy().MutatingTools
	}
	if p.MaxConsecutiveFailures <= 0 {
		p.MaxConsecutiveFailures = DefaultCompletionPolicy().MaxConsecutiveFailures
	}
	return p
}

// isMutating reports whether a tool call makes the run state-changing.
func (p CompletionPolicy) isMutating(tool string) bool {
	for _, t := range p.MutatingTools {
		if t == tool {
			return true
		}
	}
	return false
}

// verifierVerdict is the subset of the verifier tool's JSON payload the gate
// depends on. A result that cannot be parsed counts as a FAILURE, never as a
// pass: an evidence gate that treats "unreadable" as "fine" is not a gate.
type verifierVerdict struct {
	Pass    bool   `json:"pass"`
	Outcome string `json:"outcome"`
}

// parseVerifierVerdict reports whether a verifier tool result says the check
// passed. toolErr is the error the executor returned, if any.
func parseVerifierVerdict(payload string, toolErr error) bool {
	if toolErr != nil {
		return false
	}
	var v verifierVerdict
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return false
	}
	return v.Pass && v.Outcome == "passed"
}

// gateActive reports whether the evidence requirement applies to this run.
func (a *Agent) gateActive() bool {
	if !a.cfg.Completion.Enabled {
		return false
	}
	for _, t := range a.tools.Available() {
		if t.Function.Name == a.cfg.Completion.VerifierTool {
			return true
		}
	}
	return false
}

// gateCompletion is called when the model has stopped requesting tools. It
// returns an error when the run changed state without proving the change.
func (a *Agent) gateCompletion(r *RunResult) error {
	if !a.gateActive() || !r.Mutated || r.VerifierPassed {
		return nil
	}
	r.NeedsHumanApproval = true
	r.Err = ErrNoVerifierEvidence
	return ErrNoVerifierEvidence
}
