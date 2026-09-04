// Package lint enforces this repository's golangci-lint invariants as
// ordinary Go tests, so a regression in code quality fails the same
// build-and-test job as a regression in behavior.
//
// Parsing lives here, apart from the tests that invoke golangci-lint,
// because it is pure string handling. That makes every counting rule
// verifiable against golden fixtures without running the linter — and
// therefore without taking golangci-lint's host-wide lock, which is shared
// with CI and with any other session on the build host.
package lint

import (
	"regexp"
	"strconv"
	"strings"
)

// LinterAbsent is returned by CountIssues when the output carries no
// evidence that the named linter ran at all.
//
// It is deliberately distinct from 0. golangci-lint prints neither a tally
// nor an issue line for a linter that found nothing, and prints exactly
// the same nothing for a linter that was never enabled. Reporting both as
// 0 is what allowed a silenced linter to satisfy an invariant: remove
// `- gosec` from .golangci.yml and a `gosec < 10` assertion passes having
// measured nothing. Callers must resolve the ambiguity with
// EnabledLinters before treating an absence as a clean result.
const LinterAbsent = -1

// issueRe matches one golangci-lint issue line and nothing else:
//
//	internal/foo/bar_test.go:12:34: message text (errcheck)
//
// The leading path:line:column anchor is load-bearing. The `formatters`
// output echoes the offending source after each issue, and Go source
// routinely ends in a parenthesised identifier — `json.NewEncoder(w).Encode(resp)`
// ends in `(resp)`. An unanchored `\((\w+)\)$` counts those as issues
// belonging to a linter named "resp". Measured on this repository: 642
// unanchored matches against a true total of 628, inventing seven "resp",
// two "args", and one each of "wb", "agents", "pipelines", "big" and
// "expected".
var issueRe = regexp.MustCompile(`^(\S+\.go):\d+:\d+:\s.*\(([a-z][a-z0-9]*)\)$`)

// summaryRe matches the per-linter tally golangci-lint prints beneath the
// issue list, e.g. "* errcheck: 219".
var summaryRe = regexp.MustCompile(`^\s*\*\s*([a-z][a-z0-9]*):\s+(\d+)\s*$`)

// totalRe matches the header of that tally, e.g. "628 issues:".
var totalRe = regexp.MustCompile(`^(\d+) issues:`)

// CountIssues returns how many issues golangci-lint attributed to linter,
// or LinterAbsent when the run shows no sign of it.
//
// The per-linter tally is authoritative when present; otherwise issues are
// counted from anchored issue lines, which covers a truncated run or a
// tally golangci-lint declines to print.
func CountIssues(out, linter string) int {
	for _, line := range strings.Split(out, "\n") {
		if m := summaryRe.FindStringSubmatch(line); m != nil && m[1] == linter {
			n, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			return n
		}
	}

	count := 0
	for _, line := range strings.Split(out, "\n") {
		if m := issueRe.FindStringSubmatch(line); m != nil && m[2] == linter {
			count++
		}
	}
	if count == 0 {
		return LinterAbsent
	}
	return count
}

// CountIssuesInTestFiles returns the number of issues attributed to linter
// whose file is a Go test file.
//
// Both halves of that sentence matter. The predecessor of this function
// counted every output line whose text contained "_test.go:", regardless
// of which linter raised it, from a scan given `--tests=false` and so
// containing no test files at all. On this repository that filter selects
// 202 lines spanning eight linters, while the errcheck population it
// claimed to measure is 94.
func CountIssuesInTestFiles(out, linter string) int {
	count := 0
	for _, line := range strings.Split(out, "\n") {
		m := issueRe.FindStringSubmatch(line)
		if m == nil || m[2] != linter {
			continue
		}
		if strings.HasSuffix(m[1], "_test.go") {
			count++
		}
	}
	return count
}

// CountedIssueLines returns how many issue lines the parser recognized
// across every linter. Compared against TotalIssues it forms the harness's
// positive control: a parser that has drifted from golangci-lint's output
// format reports 0 for every linter, which would otherwise satisfy every
// ceiling in this package simultaneously.
func CountedIssueLines(out string) int {
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if issueRe.MatchString(line) {
			count++
		}
	}
	return count
}

// TotalIssues reports the count from golangci-lint's tally header and
// whether that header was present at all.
func TotalIssues(out string) (int, bool) {
	for _, line := range strings.Split(out, "\n") {
		if m := totalRe.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			return n, true
		}
	}
	return 0, false
}

// EnabledLinters parses `golangci-lint linters` and returns the set the
// configuration actually enables. This is what turns LinterAbsent from an
// ambiguity into a verdict: absent-and-enabled is a clean zero, whereas
// absent-and-not-enabled means the invariant is guarding nothing.
func EnabledLinters(listing string) map[string]bool {
	enabled := map[string]bool{}
	inEnabledSection := false
	for _, line := range strings.Split(listing, "\n") {
		switch {
		case strings.HasPrefix(line, "Enabled by your configuration"):
			inEnabledSection = true
			continue
		case strings.HasPrefix(line, "Disabled by your configuration"):
			inEnabledSection = false
			continue
		}
		if !inEnabledSection {
			continue
		}
		name, _, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name != "" && !strings.Contains(name, " ") {
			enabled[name] = true
		}
	}
	return enabled
}
