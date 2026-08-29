package builtins

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
)

// TestVerifierOutcomesAreAcceptedLabels is the cross-package contract check for
// the verifier half.
//
// Each side asserting its own copy of "pass"/"fail"/"error" is how two halves
// stay green while disagreeing; this compares the producer's constants against
// the consumer's accepted set.
func TestVerifierOutcomesAreAcceptedLabels(t *testing.T) {
	accepted := map[string]struct{}{}
	for _, o := range agentmetrics.AcceptedVerifierOutcomes() {
		accepted[o] = struct{}{}
	}
	emitted := VerifierOutcomes()
	if len(emitted) == 0 {
		t.Fatal("VerifierOutcomes() is empty; the producer reports nothing")
	}
	for _, o := range emitted {
		if _, ok := accepted[o]; !ok {
			t.Errorf("verifier emits outcome %q, which the metrics side folds away; the two have drifted", o)
		}
	}
	if len(emitted) != len(accepted) {
		t.Errorf("verifier emits %d outcomes but %d are accepted", len(emitted), len(accepted))
	}
}

// TestVerifierReportsErrorWhenItCannotRun: a verifier that is unreachable must
// be as visible as one that keeps failing. An observer that only fired on the
// happy path would make "the verifier is broken" look like "nobody ran the
// verifier", and those call for opposite responses.
func TestVerifierReportsErrorWhenItCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     VerifierConfig
		args    map[string]any
		wantErr bool
	}{
		{
			name:    "no sandbox configured",
			cfg:     VerifierConfig{},
			args:    map[string]any{"check": "go_build"},
			wantErr: true,
		},
		{
			name:    "unknown check",
			cfg:     VerifierConfig{Runner: &sandbox.Runner{}},
			args:    map[string]any{"check": "rm_minus_rf"},
			wantErr: true,
		},
		{
			name:    "malformed args",
			cfg:     VerifierConfig{Runner: &sandbox.Runner{}},
			args:    map[string]any{"check": "go_build", "args": "not-an-array"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			cfg := tc.cfg
			cfg.Timeout = time.Second
			cfg.OnOutcome = func(o string) { seen = append(seen, o) }
			def := VerifierTool(cfg)

			_, err := def.Handler(context.Background(), tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if len(seen) != 1 {
				t.Fatalf("observer fired %d times, want exactly 1 (%v)", len(seen), seen)
			}
			if seen[0] != VerifierOutcomeError {
				t.Errorf("outcome = %q, want %q; a verifier that never ran is not a verifier that failed", seen[0], VerifierOutcomeError)
			}
		})
	}
}

// TestVerifierWithoutObserverStillWorks is the positive control for the
// observer wiring: it must be optional, because repl and task mode register the
// same tool with no metrics behind it.
func TestVerifierWithoutObserverStillWorks(t *testing.T) {
	def := VerifierTool(VerifierConfig{})
	if _, err := def.Handler(context.Background(), map[string]any{"check": "go_build"}); err == nil {
		t.Fatal("expected the no-sandbox error")
	}
}

// TestVerifierOutcomeMapsEveryVerdict pins the pass/fail/error split.
//
// The half that matters is the last two rows. A check that TIMED OUT never
// produced a verdict, and reporting it as `fail` would let a hung suite look
// like a red suite — the exact conflation this package refuses to make in its
// result JSON. A wave of timeouts is an infrastructure problem and a wave of
// failures is a code problem; an alert that cannot tell them apart sends
// someone to read the wrong logs.
func TestVerifierOutcomeMapsEveryVerdict(t *testing.T) {
	for _, tc := range []struct {
		outcome sandbox.Outcome
		want    string
	}{
		{sandbox.OutcomePassed, VerifierOutcomePass},
		{sandbox.OutcomeFailed, VerifierOutcomeFail},
		{sandbox.OutcomeTimeout, VerifierOutcomeError},
		{sandbox.OutcomeError, VerifierOutcomeError},
		{sandbox.Outcome("something new"), VerifierOutcomeError},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			got := verifierOutcome(sandbox.Result{Outcome: tc.outcome})
			if got != tc.want {
				t.Errorf("verifierOutcome(%q) = %q, want %q", tc.outcome, got, tc.want)
			}
			if tc.outcome != sandbox.OutcomePassed && got == VerifierOutcomePass {
				t.Errorf("a non-passing verdict (%q) was reported as a pass", tc.outcome)
			}
		})
	}
}

// TestIT_VerifierReportsTheVerdictItReturns is the end-to-end control for the
// handler's reporting call.
//
// Everything above tests the mapping. None of it catches the handler simply not
// calling it: the success path needs a container, so a unit test cannot reach
// it, and a suite that only exercised the error paths would stay green while
// every real verdict went uncounted. That is the same shape as the defect this
// whole change exists to fix — a counter that exists and is never reached.
//
// It uses `file_exists` rather than a Go check so the assertion costs two quick
// container starts instead of two toolchain runs, and asserts BOTH directions:
// a satisfied check reports pass, an unsatisfied one reports fail.
func TestIT_VerifierReportsTheVerdictItReturns(t *testing.T) {
	requirePodmanForVerifier(t)
	runner, err := sandbox.NewRunner(sandbox.Config{
		Enabled:        true,
		Workspace:      verifierITWorkspace(t, false),
		Timeout:        2 * time.Minute,
		MaxOutputBytes: 4096,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	var seen []string
	tool := VerifierTool(VerifierConfig{
		Runner:    runner,
		Timeout:   2 * time.Minute,
		OnOutcome: func(o string) { seen = append(seen, o) },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"a file that exists", "go.mod", VerifierOutcomePass},
		{"a file that does not", "definitely-not-here.txt", VerifierOutcomeFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(seen)
			raw, err := tool.Handler(ctx, map[string]any{"check": "file_exists", "args": []any{tc.path}})
			if err != nil {
				t.Fatalf("verifier_run file_exists %s: %v", tc.path, err)
			}
			var decoded VerifierResult
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
				t.Fatalf("decode %q: %v", raw, err)
			}
			if len(seen) != before+1 {
				t.Fatalf("the handler reported %d outcomes for one invocation, want exactly 1", len(seen)-before)
			}
			if got := seen[len(seen)-1]; got != tc.want {
				t.Errorf("reported %q, want %q (verdict outcome=%q pass=%v)", got, tc.want, decoded.Outcome, decoded.Pass)
			}
			if (decoded.Pass) != (tc.want == VerifierOutcomePass) {
				t.Errorf("the counter and the agent's verdict disagree: reported %q, JSON pass=%v", tc.want, decoded.Pass)
			}
		})
	}
}

// TestVerifierOutcomeAgreesWithTheEncodedResult: the counter and the JSON the
// agent reads must not disagree about whether a check passed.
func TestVerifierOutcomeAgreesWithTheEncodedResult(t *testing.T) {
	for _, outcome := range []sandbox.Outcome{
		sandbox.OutcomePassed, sandbox.OutcomeFailed, sandbox.OutcomeTimeout, sandbox.OutcomeError,
	} {
		res := sandbox.Result{Outcome: outcome}
		encoded, err := encodeVerifierResult("go_test", res, 1024)
		if err != nil {
			t.Fatalf("encodeVerifierResult: %v", err)
		}
		var decoded VerifierResult
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		reportedPass := verifierOutcome(res) == VerifierOutcomePass
		if reportedPass != decoded.Pass {
			t.Errorf("outcome %q: counter says pass=%v but the agent reads pass=%v", outcome, reportedPass, decoded.Pass)
		}
	}
}
