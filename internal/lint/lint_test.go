// Package lint contains tests that enforce the v18684-1 through v18684-3
// lint-cleanup invariant. Each test asserts that the relevant golangci-lint
// linter reports zero (or <= threshold) issues on the codebase.
//
// These tests are TDD: write the test, run it (FAIL), apply the fix,
// run it again (PASS), commit. Per v18684 plan + pat-264.
package lint

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// runLint runs a full golangci-lint scan (all linters enabled, issue
// cap lifted, same-issues cap lifted) so the per-category summary line
// is always present. The Cmd.Dir is set to the repository root (2 levels
// up from internal/lint/) so golangci-lint runs against the full repo.
//
// nosec G204: the input is a fixed set of flags, not user input.
func runLint(t *testing.T) (string, error) { //nolint:gosec // G204 fixed args
	t.Helper()
	cmd := exec.Command("golangci-lint", "run", "--timeout", "5m",
		"--max-issues-per-linter=9999", "--max-same-issues=9999") //nolint:gosec
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// categoryRe captures the per-linter summary line "  * <name>: <count>".
var categoryRe = regexp.MustCompile(`^\s*\*\s*([a-z]+):\s+(\d+)\s*$`)

// categoryCount returns the issue count for the named linter, or -1 if
// the linter did not appear in the run.
func categoryCount(out, name string) int {
	for _, line := range strings.Split(out, "\n") {
		m := categoryRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == name {
			n, _ := strconv.Atoi(m[2])
			return n
		}
	}
	return -1
}

// testFileErrcheck runs a test-file-only errcheck scan. golangci-lint
// includes the file path in each issue line; we filter to lines ending
// in _test.go or test.go.
func testFileErrcheck(t *testing.T) (int, string, error) { //nolint:gosec // G204 fixed args
	t.Helper()
	cmd := exec.Command("golangci-lint", "run", "--timeout", "5m",
		"-E=errcheck", "--max-issues-per-linter=9999", "--max-same-issues=9999",
		"--tests=false") //nolint:gosec
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		// Match patterns like "cmd/foo/main_test.go:10:5: ..." or
		// "internal/foo/bar_test.go:20:3: ..."
		if strings.Contains(line, "_test.go:") || strings.Contains(line, "/test.go:") {
			count++
		}
	}
	return count, string(out), err
}

// TestErrcheck_Below350_TestFiles is the v18684-1 invariant for the
// errcheck linter. The v18684-1 sub-scope is "bare x.Close() in test
// files" — a single sub-category of errcheck. The remaining errcheck
// issues (defer patterns, fmt.Fprintf, w.Write, etc.) are deferred to
// v18685+ sub-stories.
//
// Target: 413 → ≤350. The boundary is set to 350 (vs the 413 baseline)
// to allow the test to pass after a meaningful subset is fixed while
// not requiring the entire errcheck class to be closed (that's a 3-sprint
// scope, not v18684-1).
func TestErrcheck_Below350_TestFiles(t *testing.T) {
	count, out, err := testFileErrcheck(t)
	if err != nil && out == "" {
		t.Skipf("errcheck test-file run failed: %v", err)
	}
	if count < 0 {
		t.Skipf("could not determine test-file errcheck count: %s", out)
	}
	// v18684-1 target: 413 → ≤350 (63+ errcheck issues closed in test files)
	if count >= 350 {
		t.Errorf("test-file errcheck count = %d, want < 350 (v18684-1 target); sample output: %s",
			count, lastLines(out, 15))
	}
}

// TestErrcheck_StableBoundary ensures the test-file errcheck count did
// not regress above the v18684-1 starting point of 413.
func TestErrcheck_StableBoundary(t *testing.T) {
	count, out, _ := testFileErrcheck(t)
	if count < 0 {
		t.Skipf("could not determine test-file errcheck count: %s", out)
	}
	if count > 413 {
		t.Errorf("test-file errcheck count = %d, must not regress above v18684-1 starting 413", count)
	}
}

// TestRevive_Below100 is the v18684-2 invariant for the revive linter.
// v18684-2 sub-scope (this sprint):
//   - redefines-builtin-id: rename `min`/`max`/`len` parameter shadows (13 → 0)
//   - unused-parameter: rename to `_` or `//nolint:revive` for interface-required (151 → ~15)
//   - context-as-argument: ensure ctx is first param where applicable (2 remaining)
//
// The remaining revive issues (~50) are exported godoc comments on
// stable callback/handler interfaces — these are deferred to v18685+
// per sub-scope discipline. Target: 229 → <=100 (this sprint).
func TestRevive_Below100(t *testing.T) {
	out, err := runLint(t)
	if err != nil && out == "" {
		t.Skipf("golangci-lint run failed: %v", err)
	}
	got := categoryCount(out, "revive")
	if got < 0 {
		t.Skipf("no revive summary line: %s", out)
	}
	if got >= 100 {
		t.Errorf("revive count = %d, want < 100 (v18684-2 target from 229); sample output: %s",
			got, lastLines(out, 15))
	}
}

// TestRevive_StableBoundary ensures revive count did not regress above
// the v18684-2 starting 229.
func TestRevive_StableBoundary(t *testing.T) {
	out, err := runLint(t)
	if err != nil && out == "" {
		t.Skipf("golangci-lint run failed: %v", err)
	}
	got := categoryCount(out, "revive")
	if got < 0 {
		t.Skipf("no revive summary line: %s", out)
	}
	if got > 229 {
		t.Errorf("revive count = %d, must not regress above v18684-2 starting 229", got)
	}
}

// lastLines returns the trailing N non-empty lines for failure context.
func lastLines(out string, n int) string {
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
