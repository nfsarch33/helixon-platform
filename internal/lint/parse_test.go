package lint

import "testing"

// realOutput is a faithful excerpt of `golangci-lint run` against this
// repository: a handful of issue lines, the echoed source that
// `print-issued-lines` interleaves between them, and the trailing tally.
// Keeping a real sample here is what makes the parser verifiable without
// invoking golangci-lint at all — and therefore without taking its
// host-wide lock.
const realOutput = `cmd/choose-llm/hook_test.go:135:12: Error return value of ` + "`os.Setenv`" + ` is not checked (errcheck)
	os.Setenv("HOME", dir)
	^
cmd/choose-llm/main.go:152:15: Error return value of ` + "`fmt.Fprintf`" + ` is not checked (errcheck)
	fmt.Fprintf(os.Stderr, "boom")
	^
internal/console/server.go:440:24: response body must be closed (bodyclose)
		json.NewEncoder(w).Encode(resp)
		^
internal/console/server.go:389:24: response body must be closed (bodyclose)
		json.NewEncoder(w).Encode(agents)
		^
internal/fleet/mirror_test.go:208:25: Error return value is not checked (errcheck)
	defer f.Close()
	^
5 issues:
* bodyclose: 2
* errcheck: 3
`

// TestIssueRe_IgnoresEchoedSourceLines pins the defect that the anchor
// exists for. `print-issued-lines: true` echoes the offending source after
// each issue, and real source lines end in `(resp)` / `(agents)`. An
// unanchored `\((\w+)\)$` — the shape this harness used to carry — counts
// those as issues belonging to linters named "resp" and "agents".
// Measured on the real repository: 642 unanchored matches against a true
// total of 628.
func TestIssueRe_IgnoresEchoedSourceLines(t *testing.T) {
	t.Parallel()
	for _, linter := range []string{"resp", "agents"} {
		if got := CountIssues(realOutput, linter); got != LinterAbsent {
			t.Errorf("CountIssues(%q) = %d, want LinterAbsent; an echoed source line was counted as an issue",
				linter, got)
		}
	}
}

// TestCountIssues_PrefersTheSummaryTally checks the authoritative path:
// when golangci-lint prints a per-linter tally, that number wins.
func TestCountIssues_PrefersTheSummaryTally(t *testing.T) {
	t.Parallel()
	if got := CountIssues(realOutput, "errcheck"); got != 3 {
		t.Errorf("CountIssues(errcheck) = %d, want 3 (from the summary line)", got)
	}
	if got := CountIssues(realOutput, "bodyclose"); got != 2 {
		t.Errorf("CountIssues(bodyclose) = %d, want 2 (from the summary line)", got)
	}
}

// TestCountIssues_AbsentLinterIsNotZero is the heart of the fail-closed
// contract. golangci-lint prints neither a tally nor an issue line for a
// linter that found nothing — and prints exactly the same nothing for a
// linter that was never enabled. Returning 0 for both is what let a
// silenced linter read as a satisfied invariant: delete `- gosec` from
// .golangci.yml and a `gosec < 10` assertion passes while measuring
// nothing at all.
func TestCountIssues_AbsentLinterIsNotZero(t *testing.T) {
	t.Parallel()
	if got := CountIssues(realOutput, "gosec"); got != LinterAbsent {
		t.Errorf("CountIssues(gosec) = %d, want LinterAbsent (%d); "+
			"a linter that produced no evidence must not be reported as a clean 0",
			got, LinterAbsent)
	}
}

// TestCountIssues_CountsIssueLinesWithoutATally covers the case where a
// linter emits issues but the tally is missing (truncated output, or a
// format golangci-lint changes under us).
func TestCountIssues_CountsIssueLinesWithoutATally(t *testing.T) {
	t.Parallel()
	const noTally = `internal/a/x.go:1:1: something (gosec)
internal/b/y.go:2:2: something else (gosec)
`
	if got := CountIssues(noTally, "gosec"); got != 2 {
		t.Errorf("CountIssues(gosec) = %d, want 2 counted from issue lines", got)
	}
}

// TestCountIssuesInTestFiles_FiltersByLinterAndPath pins the defect that
// made two of this package's invariants unfalsifiable. The old harness
// counted every output line whose path contained `_test.go:`, regardless
// of which linter raised it, from a scan that had been given
// `--tests=false` and therefore contained no test files at all. Measured
// on the real repository: that filter selects 202 lines spanning errcheck,
// misspell, gocritic, bodyclose and four more, while the errcheck count it
// claims to be is 94.
func TestCountIssuesInTestFiles_FiltersByLinterAndPath(t *testing.T) {
	t.Parallel()
	if got := CountIssuesInTestFiles(realOutput, "errcheck"); got != 2 {
		t.Errorf("CountIssuesInTestFiles(errcheck) = %d, want 2 "+
			"(hook_test.go and mirror_test.go, not the main.go finding)", got)
	}
	if got := CountIssuesInTestFiles(realOutput, "bodyclose"); got != 0 {
		t.Errorf("CountIssuesInTestFiles(bodyclose) = %d, want 0; "+
			"neither bodyclose finding is in a test file", got)
	}
}

// TestTotalIssues_ReadsTheHeader covers the positive control's input.
func TestTotalIssues_ReadsTheHeader(t *testing.T) {
	t.Parallel()
	got, ok := TotalIssues(realOutput)
	if !ok {
		t.Fatal("TotalIssues found no header; the positive control cannot run")
	}
	if got != 5 {
		t.Errorf("TotalIssues = %d, want 5", got)
	}
	if _, ok := TotalIssues("no header here\n"); ok {
		t.Error("TotalIssues reported a header in output that has none")
	}
}

// TestCountedIssueLines_MatchesTheHeader is the parser's self-check, and
// the same assertion the live harness applies to a real run. If
// golangci-lint's output format drifts, the anchored regex stops matching
// and this disagreement is how we find out — rather than every linter
// silently counting 0 and every invariant passing.
func TestCountedIssueLines_MatchesTheHeader(t *testing.T) {
	t.Parallel()
	total, ok := TotalIssues(realOutput)
	if !ok {
		t.Fatal("no header in the fixture")
	}
	if got := CountedIssueLines(realOutput); got != total {
		t.Errorf("CountedIssueLines = %d but the header says %d; the parser and golangci-lint disagree",
			got, total)
	}
}

// TestEnabledLinters_ParsesTheListing covers the enablement probe that
// makes LinterAbsent actionable: absent-and-enabled is a clean 0,
// absent-and-not-enabled is a broken gate.
func TestEnabledLinters_ParsesTheListing(t *testing.T) {
	t.Parallel()
	const listing = `Enabled by your configuration linters:
errcheck: Errcheck is a program for checking for unchecked errors in Go code.
gosec: Inspects source code for security problems.
revive: Fast, configurable, extensible, flexible, and beautiful linter for Go. [auto-fix]
Disabled by your configuration linters:
dupl: Tool for code clone detection.
`
	got := EnabledLinters(listing)
	for _, want := range []string{"errcheck", "gosec", "revive"} {
		if !got[want] {
			t.Errorf("EnabledLinters missing %q", want)
		}
	}
	if got["dupl"] {
		t.Error("EnabledLinters reported a linter from the Disabled section")
	}
}
