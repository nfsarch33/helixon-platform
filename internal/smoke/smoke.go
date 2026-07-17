// Package smoke implements the v14510 10-prompt tier smoke runner.
//
// It is intentionally separate from cmd/choose-llm so that the
// router binary stays a stateless side-car (one JSON on stdout,
// nothing more). The runner takes the output of `choose-llm pick`
// and exercises every prompt in a target tier against it.
//
// The runner supports a `--mock` mode (no HTTP at all) which is the
// path the tests use today; it will be swapped for real HTTP + the
// `internal/retry` helper once the v14508 retry policy is wired in
// (v14511 carry-forward).
package smoke

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Prompt is one entry of the 10-prompt smoke fixture.
type Prompt struct {
	ID     string `json:"id"`
	Tier   int    `json:"tier"`
	Prompt string `json:"prompt"`
	Rubric Rubric `json:"rubric"`
}

// Rubric captures the scoring criteria for a single prompt. Multiple
// checks are AND'd; the response must pass every present check.
type Rubric struct {
	ContainsSubstrings    []string `json:"contains_substrings,omitempty"`
	ContainsSubstringsAny []string `json:"contains_substrings_any,omitempty"`
	MaxWords              int      `json:"max_words,omitempty"`
	MinWords              int      `json:"min_words,omitempty"`
	MinNewlines           int      `json:"min_newlines,omitempty"`
	MaxCompletionTokens   int      `json:"max_completion_tokens,omitempty"`
	Regex                 string   `json:"regex,omitempty"`
	JSONArrayMinLen       int      `json:"json_array_min_len,omitempty"`
	ExactJSON             any      `json:"exact_json,omitempty"`
	MaxLatencyMs          int      `json:"max_latency_ms,omitempty"`
}

// LoadPromptsFile reads the JSON fixture; the runner ships one
// fixture at eval-harness/prompts-10.json and the smoke command
// allows --prompts to point at a custom file for operator-time
// prompt-set revision.
func LoadPromptsFile(p string) ([]Prompt, error) {
	raw, err := os.ReadFile(p) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		return nil, fmt.Errorf("read prompts %q: %w", p, err)
	}
	var ps []Prompt
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, fmt.Errorf("parse prompts: %w", err)
	}
	return ps, nil
}

// Accepts is the rubric evaluation function. It is package-local so
// other tests can verify each check independently. The response text
// is the model's raw content; token / latency checks use the
// pre-computed fields on Result.
//
// Decomposed from a 24-CC monolith into 8 focused check functions
// (CC ≤ 3 each) for tech-debt-block-8. The orchestrator runs all
// checks in order and short-circuits on the first failure (early
// return is the v14510 spec).
func (r Rubric) Accepts(content string) bool {
	lc := strings.ToLower(content)
	for _, check := range r.checks() {
		if !check(lc, content) {
			return false
		}
	}
	return true
}

// checks returns the ordered list of validation check functions. Each
// function returns true when the content passes that check. Adding a
// new Rubric field is a one-line addition here.
func (r Rubric) checks() []func(lc, content string) bool {
	out := []func(lc, content string) bool{}
	if len(r.ContainsSubstrings) > 0 {
		out = append(out, checkContainsSubstrings(r.ContainsSubstrings))
	}
	if len(r.ContainsSubstringsAny) > 0 {
		out = append(out, checkContainsSubstringsAny(r.ContainsSubstringsAny))
	}
	if r.MaxWords > 0 {
		out = append(out, checkMaxWords(r.MaxWords))
	}
	if r.MinWords > 0 {
		out = append(out, checkMinWords(r.MinWords))
	}
	if r.MinNewlines > 0 {
		out = append(out, checkMinNewlines(r.MinNewlines))
	}
	if r.MaxCompletionTokens > 0 {
		out = append(out, checkMaxCompletionTokens(r.MaxCompletionTokens))
	}
	if r.Regex != "" {
		out = append(out, checkRegex(r.Regex))
	}
	if r.JSONArrayMinLen > 0 {
		out = append(out, checkJSONArrayMinLen(r.JSONArrayMinLen))
	}
	if r.ExactJSON != nil {
		out = append(out, checkExactJSON(r.ExactJSON))
	}
	return out
}

// checkContainsSubstrings verifies the content contains every required
// substring (case-insensitive). CC=2.
func checkContainsSubstrings(needles []string) func(lc, _ string) bool {
	return func(lc, _ string) bool {
		for _, s := range needles {
			if !strings.Contains(lc, strings.ToLower(s)) {
				return false
			}
		}
		return true
	}
}

// checkContainsSubstringsAny verifies the content contains at least
// one of the allowed substrings (case-insensitive). CC=3.
func checkContainsSubstringsAny(needles []string) func(lc, _ string) bool {
	return func(lc, _ string) bool {
		for _, s := range needles {
			if strings.Contains(lc, strings.ToLower(s)) {
				return true
			}
		}
		return false
	}
}

// checkMaxWords fails when the content has more than `maxWords` words. CC=2.
func checkMaxWords(maxWords int) func(_, content string) bool {
	return func(_, content string) bool {
		return wordCount(content) <= maxWords
	}
}

// checkMinWords fails when the content has fewer than `minWords` words. CC=2.
func checkMinWords(minWords int) func(_, content string) bool {
	return func(_, content string) bool {
		return wordCount(content) >= minWords
	}
}

// checkMinNewlines fails when the content has fewer than `minNewlines`
// newline characters. CC=2.
func checkMinNewlines(minNewlines int) func(_, content string) bool {
	return func(_, content string) bool {
		return strings.Count(content, "\n") >= minNewlines
	}
}

// checkMaxCompletionTokens fails when the content has more than
// `maxTokens` words (proxy for completion tokens). CC=2.
func checkMaxCompletionTokens(maxTokens int) func(_, content string) bool {
	return func(_, content string) bool {
		return wordCount(content) <= maxTokens
	}
}

// checkRegex fails when the content does not match the regex. CC=3.
func checkRegex(pattern string) func(_, content string) bool {
	re, err := regexp.Compile(pattern)
	return func(_, content string) bool {
		if err != nil {
			return false
		}
		return re.MatchString(strings.TrimSpace(content))
	}
}

// checkJSONArrayMinLen fails when the content is not a JSON array of
// at least `minLen` elements. CC=3.
func checkJSONArrayMinLen(minLen int) func(_, content string) bool {
	return func(_, content string) bool {
		var arr []any
		if err := json.Unmarshal([]byte(content), &arr); err != nil {
			return false
		}
		return len(arr) >= minLen
	}
}

// checkExactJSON fails when the content does not match the expected
// JSON after a round-trip marshal/unmarshal. CC=3.
func checkExactJSON(want any) func(_, content string) bool {
	bb, _ := json.Marshal(want)
	var w any
	_ = json.Unmarshal(bb, &w)
	wb, _ := json.Marshal(w)
	return func(_, content string) bool {
		var got any
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			return false
		}
		gb, _ := json.Marshal(got)
		return string(gb) == string(wb)
	}
}

// Result is one row of the smoke output; it mirrors the existing
// qwen36-eval-smoke.json shape (see cursor-global-kb/scripts/fleet/qwen36-eval-smoke.sh)
// so the Grafana dashboard can join across the two evidence sets.
type Result struct {
	ID         string `json:"id"`
	Tier       int    `json:"tier"`
	Passed     bool   `json:"passed"`
	Reason     string `json:"reason,omitempty"`
	CellID     string `json:"cell_id"`
	BaseURL    string `json:"base_url,omitempty"`
	LatencyMs  int    `json:"latency_ms"`
	TokensUsed int    `json:"tokens_used,omitempty"`
}

// Scoreboard is the aggregate of all Result rows.
type Scoreboard struct {
	Total    int               `json:"total"`
	Passed   int               `json:"passed"`
	ByTier   map[int]TierScore `json:"by_tier"`
	CellsHit map[string]int    `json:"cells_hit"`
}

// TierScore is one row of the per-tire scoreboard.
type TierScore struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
}

// Percentage is the percentage of prompts that passed. Boring math
// kept here so callers do not have to special-case empty scores.
func (s Scoreboard) Percentage() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Passed) * 100 / float64(s.Total)
}

// Aggregate walks the per-prompt results and folds them into a
// Scoreboard. By-tier counts are always populated even when no
// prompts landed in that tier (we still want the JSON shape stable
// for downstream Grafana queries).
func Aggregate(results []Result) Scoreboard {
	byTier := map[int]TierScore{
		0: {},
		1: {},
		2: {},
		3: {},
	}
	cells := map[string]int{}
	passed := 0
	for _, r := range results {
		ts := byTier[r.Tier]
		ts.Total++
		if r.Passed {
			ts.Passed++
			passed++
		}
		byTier[r.Tier] = ts
		if r.CellID != "" {
			cells[r.CellID]++
		}
	}
	return Scoreboard{
		Total:    len(results),
		Passed:   passed,
		ByTier:   byTier,
		CellsHit: cells,
	}
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}
