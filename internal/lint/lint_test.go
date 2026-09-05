// Invariant tests for this repository's golangci-lint ceilings.
//
// Each test asserts that a linter's issue count stays at or below a
// measured ceiling. The ceilings are ratchets: lower one whenever the
// count drops, and raise one only with a recorded justification in the
// commit that raises it.
//
// Everything here runs golangci-lint exactly once per `go test` and shares
// the output. The parsing rules live in parse.go and are unit-tested
// against golden fixtures in parse_test.go, so the counting logic is
// verifiable without invoking the linter at all.
package lint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// repoRoot is where golangci-lint is invoked from: two levels up from
// internal/lint. assertRepoRoot proves that assumption rather than
// trusting it, because a wrong directory yields a scan of nothing, and a
// scan of nothing would satisfy every ceiling in this file at once.
const repoRoot = "../.."

// Ceilings, measured on this repository. See the ratchet note above.
const (
	// gosecCeiling: measured 0. The headroom absorbs a single transient
	// finding without hiding a class regression; gosec at 0 is carried by
	// per-line //nolint:gosec directives, so a jump means either a new
	// unannotated sink or a directive that stopped applying.
	gosecCeiling = 5

	// reviveCeiling: measured 0, from a baseline of 229 closed in v18684-2.
	reviveCeiling = 10

	// errcheckTestFileCeiling: measured 94. The headroom is wider because
	// this population grows whenever tests are added, and a ceiling that
	// tracks the count exactly would fail unrelated pull requests that add
	// test coverage.
	errcheckTestFileCeiling = 110
)

var (
	lintOnce sync.Once
	lintOut  string
	lintErr  error
	listOnce sync.Once
	listOut  string
	listErr  error
)

// runLint scans the whole repository once and shares the result.
//
// One scan, not one per test. Each invocation takes golangci-lint's
// host-wide lock and is authorized for five minutes, while `go test`
// allows a package ten minutes in total: the previous harness issued six
// scans from six tests and could therefore exhaust the package deadline,
// which surfaces as `panic: test timed out` and reads like a failing
// invariant rather than a harness that asked for too much.
func runLint(t *testing.T) (string, error) {
	t.Helper()
	lintOnce.Do(func() {
		logLintBinary(t)
		// --allow-parallel-runners: golangci-lint serializes on a lock at a
		// fixed path shared by every process on the host. Without this flag
		// a scan started while CI, another agent session, or a developer's
		// `task lint` holds that lock exits 3 with "parallel golangci-lint
		// is running" — a red caused by a neighbor rather than by this
		// repository. Measured with the lock held throughout: without the
		// flag exit 3 and no verdict; with it, a verdict whose per-linter
		// counts are byte-identical to an uncontended run.
		cmd := exec.Command("golangci-lint", "run", //nolint:gosec // G204 fixed args
			"--timeout", "5m",
			"--allow-parallel-runners",
			"--max-issues-per-linter=9999",
			"--max-same-issues=9999")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		lintOut, lintErr = string(out), err
	})
	requireLintVerdict(t, lintOut, lintErr)
	return lintOut, lintErr
}

// enabledLinters asks golangci-lint which linters this configuration
// actually turns on, once per `go test`.
func enabledLinters(t *testing.T) (map[string]bool, error) {
	t.Helper()
	listOnce.Do(func() {
		cmd := exec.Command("golangci-lint", "linters") //nolint:gosec // G204 fixed args
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		listOut, listErr = string(out), err
	})
	if listErr != nil {
		return nil, listErr
	}
	return EnabledLinters(listOut), nil
}

// logLintBinary records which golangci-lint ran and at what version, so a
// count that differs between two environments is attributable to a binary
// rather than guessed at.
//
// Only the file name is logged. This repository is public and `go test`
// prints a failing test's logs into CI output, so the absolute path —
// which contains the operator's home directory on every host in this
// fleet — must not appear there.
func logLintBinary(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("golangci-lint")
	if err != nil {
		return
	}
	ver, err := exec.Command(path, "version").Output() //nolint:gosec // G204 fixed args on the resolved binary
	if err != nil {
		return
	}
	t.Logf("%s: %s", filepath.Base(path), strings.TrimSpace(string(ver)))
}

// requireLintVerdict fails the test when golangci-lint terminated without
// rendering a lint verdict. Exit 0 (clean) and exit 1 (issues found) are
// verdicts; anything else — a rejected config, a run error, a timeout, or
// a signal kill, whose truncated output would parse as a low count — would
// otherwise be counted as "no issues" and turn a broken linter into a
// satisfied invariant.
func requireLintVerdict(t *testing.T, out string, err error) {
	t.Helper()
	var ee *exec.ExitError
	if err != nil && errors.As(err, &ee) && ee.ExitCode() != 1 {
		t.Fatalf("golangci-lint exited %d without rendering a lint verdict; output: %s",
			ee.ExitCode(), lastLines(out))
	}
}

// assertRepoRoot proves cmd.Dir points at the module root.
func assertRepoRoot(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: the scan would cover nothing and every ceiling "+
			"in this package would pass vacuously (%v)", repoRoot, err)
	}
}

// ceiling is the shared body of every invariant test: prove the harness
// measured something, then compare one linter's count against its ceiling.
func ceiling(t *testing.T, linter string, limit int, count func(string) int) {
	t.Helper()
	assertRepoRoot(t)
	out, err := runLint(t)
	if err != nil && out == "" {
		t.Fatalf("golangci-lint produced no output at all: %v", err)
	}

	enabled, err := enabledLinters(t)
	if err != nil {
		t.Fatalf("cannot determine which linters are enabled, so a zero count is "+
			"unreadable: %v", err)
	}
	if !enabled[linter] {
		t.Fatalf("%s is not enabled by .golangci.yml; this invariant is guarding nothing",
			linter)
	}

	got := count(out)
	if got == LinterAbsent {
		// Enabled, and no evidence in the output: a genuine clean result.
		got = 0
	}
	if got > limit {
		t.Errorf("%s count = %d, ceiling is %d; sample output: %s",
			linter, got, limit, lastLines(out))
	}
	t.Logf("%s = %d (ceiling %d)", linter, got, limit)
}

// TestHarness_ParsedTheRun is the positive control for every ceiling in
// this file.
//
// A gate built only from "count <= ceiling" assertions passes when the
// measurement returns nothing — which is exactly what a drifted output
// format, a wrong working directory, or a linter that never ran all look
// like. This test asserts the opposite direction: the parser recognized
// the same number of issues golangci-lint says it reported. If those
// disagree, every ceiling below is meaningless and this says so.
func TestHarness_ParsedTheRun(t *testing.T) {
	assertRepoRoot(t)
	out, err := runLint(t)
	if err != nil && out == "" {
		t.Fatalf("golangci-lint produced no output at all: %v", err)
	}

	total, ok := TotalIssues(out)
	if !ok {
		// No tally header is only legitimate on a completely clean tree.
		if n := CountedIssueLines(out); n != 0 {
			t.Fatalf("no issue tally in the output, yet %d issue lines parsed; "+
				"the output format has changed under this harness", n)
		}
		t.Log("clean tree: no issues reported")
		return
	}
	if got := CountedIssueLines(out); got != total {
		t.Fatalf("parsed %d issue lines but golangci-lint reports %d; the parser and "+
			"the linter disagree, so every ceiling in this package is unreliable",
			got, total)
	}
	t.Logf("parser agrees with golangci-lint: %d issues", total)
}

// TestGosec_Ceiling is the v18684-3 invariant: security findings stay
// closed. Baseline 129, reduced to 0 in v18802.
func TestGosec_Ceiling(t *testing.T) {
	ceiling(t, "gosec", gosecCeiling, func(out string) int {
		return CountIssues(out, "gosec")
	})
}

// TestRevive_Ceiling is the v18684-2 invariant. Baseline 229, now 0.
func TestRevive_Ceiling(t *testing.T) {
	ceiling(t, "revive", reviveCeiling, func(out string) int {
		return CountIssues(out, "revive")
	})
}

// TestErrcheck_TestFileCeiling is the v18684-1 invariant: unchecked errors
// in test files.
//
// This assertion was unfalsifiable from the day it was written until
// v18813. It ran the scan with `--tests=false`, which removes every
// `_test.go` file from the analysis, and then counted output lines whose
// path contained `_test.go:` — two operations that are complements, so the
// count was structurally pinned at 0 and the thresholds of 350 and 413
// could never be crossed. The real population, counted by linter tag on
// test-file paths, is 94.
func TestErrcheck_TestFileCeiling(t *testing.T) {
	ceiling(t, "errcheck", errcheckTestFileCeiling, func(out string) int {
		return CountIssuesInTestFiles(out, "errcheck")
	})
}

// lastLines returns the trailing non-empty lines for failure context.
func lastLines(out string) string {
	const n = 15
	lines := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
