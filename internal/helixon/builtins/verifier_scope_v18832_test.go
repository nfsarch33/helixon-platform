package builtins

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
)

// checkByName is a small helper so each test below reads as the assertion it is
// rather than as three lines of table lookup.
func checkByName(t *testing.T, name string) VerifierCheck {
	t.Helper()
	for _, c := range DefaultVerifierChecks() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q is not in the default table", name)
	return VerifierCheck{}
}

// TestScopeReplacesDefaultRatherThanAppending is the v18832 regression pin.
//
// The defect: Args carried both the subcommand AND the target ({"test",
// "./..."}), and a caller's extra arguments were appended, producing
// `go test ./... ./internal/foo`. That is not a scoped run — it is the whole
// tree plus the named package. The fleet workspace holds a dozen agents'
// in-progress packages at any time, so a student asking to test only its own
// package instead ran everyone's, failed on code it had never seen, and spent
// the remainder of its budget trying to fix it.
//
// The assertion that matters is the negative one: "./..." must be ABSENT once a
// scope is given. A test that only checked the pattern was present would pass
// against the defective behaviour too.
func TestScopeReplacesDefaultRatherThanAppending(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"go_build", "go_test", "go_vet"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			check := checkByName(t, name)

			bare, err := buildVerifierArgv(check, nil, sandbox.DefaultWorkspaceMount)
			if err != nil {
				t.Fatalf("buildVerifierArgv(no args): %v", err)
			}
			if got := strings.Join(bare, " "); got != check.Args[0]+" ./..." {
				t.Fatalf("unscoped argv = %q, want the default scope ./...", got)
			}

			scoped, err := buildVerifierArgv(check, []string{"./internal/foo/..."}, sandbox.DefaultWorkspaceMount)
			if err != nil {
				t.Fatalf("buildVerifierArgv(scoped): %v", err)
			}
			for _, a := range scoped {
				if a == "./..." {
					t.Fatalf("argv = %v; the default scope ./... survived alongside the caller's pattern, "+
						"so the run still covers the whole workspace", scoped)
				}
			}
			if got := strings.Join(scoped, " "); got != check.Args[0]+" ./internal/foo/..." {
				t.Fatalf("scoped argv = %q, want only the caller's pattern", got)
			}
		})
	}
}

// TestGofmtCheckArgvIsUnchanged: splitting Args from DefaultScope must not alter
// what actually runs for a check that never accepted extra arguments.
func TestGofmtCheckArgvIsUnchanged(t *testing.T) {
	t.Parallel()
	got, err := buildVerifierArgv(checkByName(t, "gofmt_check"), nil, sandbox.DefaultWorkspaceMount)
	if err != nil {
		t.Fatalf("buildVerifierArgv: %v", err)
	}
	if strings.Join(got, " ") != "-l ." {
		t.Fatalf("argv = %v, want the unchanged `gofmt -l .`", got)
	}
}

// TestApplyOutputAssertion covers the second v18832 defect: `gofmt -l` exits 0
// whether or not it found anything, so the sandbox — which derives its outcome
// from the exit code alone — reported `passed` precisely when the check had
// findings to report.
func TestApplyOutputAssertion(t *testing.T) {
	t.Parallel()
	gofmt := checkByName(t, "gofmt_check")
	goTest := checkByName(t, "go_test")

	tests := []struct {
		name        string
		check       VerifierCheck
		res         sandbox.Result
		wantOutcome sandbox.Outcome
		wantNote    bool
	}{
		{
			name:        "a lister that printed filenames FAILS despite exit 0",
			check:       gofmt,
			res:         sandbox.Result{Outcome: sandbox.OutcomePassed, ExitCode: 0, Output: "main.go\nutil.go\n"},
			wantOutcome: sandbox.OutcomeFailed,
			wantNote:    true,
		},
		{
			name:        "a lister that printed nothing passes",
			check:       gofmt,
			res:         sandbox.Result{Outcome: sandbox.OutcomePassed, ExitCode: 0, Output: ""},
			wantOutcome: sandbox.OutcomePassed,
		},
		{
			name:        "whitespace is not a finding",
			check:       gofmt,
			res:         sandbox.Result{Outcome: sandbox.OutcomePassed, ExitCode: 0, Output: "\n  \n"},
			wantOutcome: sandbox.OutcomePassed,
		},
		{
			name:        "a check without the assertion is untouched",
			check:       goTest,
			res:         sandbox.Result{Outcome: sandbox.OutcomePassed, ExitCode: 0, Output: "ok  \tpkg\t0.1s"},
			wantOutcome: sandbox.OutcomePassed,
		},
		{
			name:        "an already-failing result is not re-judged",
			check:       gofmt,
			res:         sandbox.Result{Outcome: sandbox.OutcomeFailed, ExitCode: 2, Output: "gofmt: broken"},
			wantOutcome: sandbox.OutcomeFailed,
		},
		{
			name:        "a timeout is never converted into a verdict",
			check:       gofmt,
			res:         sandbox.Result{Outcome: sandbox.OutcomeTimeout, ExitCode: -1, Output: "main.go"},
			wantOutcome: sandbox.OutcomeTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, note := applyOutputAssertion(tt.check, tt.res)
			if got.Outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, tt.wantOutcome)
			}
			if (note != "") != tt.wantNote {
				t.Errorf("note = %q, wantNote = %v", note, tt.wantNote)
			}
			if tt.wantNote && !strings.Contains(note, tt.check.Name) {
				t.Errorf("the note must name the check so the agent can act on it; got %q", note)
			}
		})
	}
}

// TestOutputAssertionKeepsCounterAndVerdictAgreeing is the cross-check.
//
// The downgrade has to happen once, upstream of both consumers. Applying it to
// the JSON but not the counter would leave an operator's dashboard green while
// the agent read a failure, and applying it to the counter but not the JSON
// would do the reverse — a worse defect than the one being fixed, because it
// would be invisible from either side alone.
func TestOutputAssertionKeepsCounterAndVerdictAgreeing(t *testing.T) {
	t.Parallel()
	gofmt := checkByName(t, "gofmt_check")
	raw := sandbox.Result{Outcome: sandbox.OutcomePassed, ExitCode: 0, Output: "main.go\n"}

	downgraded, note := applyOutputAssertion(gofmt, raw)
	counter := verifierOutcome(downgraded)
	if counter != VerifierOutcomeFail {
		t.Fatalf("counter = %q, want %q", counter, VerifierOutcomeFail)
	}

	encoded, err := encodeVerifierResultWithNote(gofmt.Name, downgraded, 4096, note)
	if err != nil {
		t.Fatalf("encodeVerifierResultWithNote: %v", err)
	}
	var decoded VerifierResult
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("decode %q: %v", encoded, err)
	}
	if decoded.Pass {
		t.Error("the agent reads pass=true for a check that found unformatted files")
	}
	if (counter == VerifierOutcomePass) != decoded.Pass {
		t.Errorf("counter says %q but the agent reads pass=%v", counter, decoded.Pass)
	}
	if decoded.ExitCode != 0 {
		t.Errorf("exit_code = %d; the real exit code must survive, or the note makes no sense", decoded.ExitCode)
	}
	if decoded.Note == "" {
		t.Error("pass=false beside exit_code=0 with no note is an unreadable verdict")
	}
	if !strings.Contains(decoded.Output, "main.go") {
		t.Errorf("the offending filenames must reach the agent; output_excerpt = %q", decoded.Output)
	}
}

// TestEncodeVerifierResultOmitsAnEmptyNote keeps the common case clean: every
// passing check would otherwise carry an empty field into the model's context,
// once per call, for no information.
func TestEncodeVerifierResultOmitsAnEmptyNote(t *testing.T) {
	t.Parallel()
	encoded, err := encodeVerifierResult("go_test", sandbox.Result{Outcome: sandbox.OutcomePassed, Output: "ok"}, 4096)
	if err != nil {
		t.Fatalf("encodeVerifierResult: %v", err)
	}
	if strings.Contains(encoded, "note") {
		t.Errorf("an empty note must be omitted from the JSON; got %s", encoded)
	}
}

// TestVerifierDescriptionTellsTheAgentHowToReadAVerdict.
//
// The tool description is the only place a model reliably reads before its first
// call. Both v18832 defects were survivable if the agent knew two things: that
// `pass` and the exit code can disagree, and that a scope replaces the default.
// Asserting the description carries them stops a later edit from quietly
// dropping the guidance while the code stays correct.
func TestVerifierDescriptionTellsTheAgentHowToReadAVerdict(t *testing.T) {
	t.Parallel()
	def := VerifierTool(VerifierConfig{})
	for _, want := range []string{"pass", "exit code", "gofmt_check", "REPLACES"} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("the verifier description must mention %q; got: %s", want, def.Description)
		}
	}
}
