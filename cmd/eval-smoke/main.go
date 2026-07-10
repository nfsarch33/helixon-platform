// Command eval-smoke is the v14510 10-prompt tier-smoke runner.
//
// It walks eval-harness/prompts-10.json, picks a cell per tier via
// choose-llm's logic (in-process, to avoid a CLI roundtrip in the
// tightest CI loop), evaluates each rubric against a deterministic
// mock response (the runner ships in --mock mode because we have no
// guarantee that C7 (Q8_0 MTP, dual RTX 3090) is loaded in a CI
// runner), aggregates a scoreboard, and emits one JSON.
//
// Exit codes:
//
//	0 -- all 10 prompts ran (some may have failed the rubric).
//	2 -- failure to load matrix or prompts file.
//	3 -- bad flag.
//
// In real-CUDA mode (--no-mock), the runner issues HTTP calls to
// the chosen cell's base_url via the internal/retry helper. v14511
// flips on that path; until then the mock path is the only one
// this binary supports.
//
// References:
//
//	eval-harness/design.md
//	eval-harness/prompts-10.json
//	internal/llm/qwen36/{matrix,router}.go
//	internal/smoke/{smoke,smoke_test}.go
//	internal/retry  (foundation for real-mode retries; v14511)
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nfsarch33/helixon-platform/internal/llm/qwen36"
	"github.com/nfsarch33/helixon-platform/internal/smoke"
)

var (
	evalVersion = "v14510.0"
	evalCommit  = "dev"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(exitCodeFor(err))
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "eval-smoke",
		Short: "Helixon 10-prompt tier smoke runner",
		Long: `eval-smoke exercises the qwen36 tier router against the 10
eval-harness prompts and emits one JSON scoreboard.

v14510 ships --mock only; v14511 flips to live HTTP via the
internal/retry helper. Run with --matrix=PATH and --prompts=PATH
to point at a custom fixture.`,
		SilenceUsage: true,
	}
	root.AddCommand(newRunCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func newRunCmd() *cobra.Command {
	var (
		matrixPath   string
		promptsPath  string
		mock         bool
		hostOverride string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute the 10-prompt smoke",
		RunE: func(cmd *cobra.Command, _ []string) error {
			prompts, err := smoke.LoadPromptsFile(promptsPath)
			if err != nil {
				return fmt.Errorf("load prompts: %w", err)
			}
			m, err := qwen36.LoadFile(matrixPath)
			if err != nil {
				return fmt.Errorf("load matrix: %w", err)
			}
			results := runMock(m, prompts)
			board := smoke.Aggregate(results)
			out := struct {
				SprintID     string           `json:"sprint_id"`
				Mock         bool             `json:"mock"`
				Matrix       string           `json:"matrix_path"`
				Prompts      string           `json:"prompts_path"`
				Scoreboard   smoke.Scoreboard `json:"scoreboard"`
				Results      []smoke.Result   `json:"results"`
				HostOverride string           `json:"host_override,omitempty"`
			}{
				SprintID:     "v14510",
				Mock:         mock,
				Matrix:       matrixPath,
				Prompts:      promptsPath,
				Scoreboard:   board,
				Results:      results,
				HostOverride: hostOverride,
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
	cmd.Flags().StringVar(&matrixPath, "matrix", defaultMatrixPath(), "path to qwen36-matrix.yaml")
	cmd.Flags().StringVar(&promptsPath, "prompts", defaultPromptsPath(), "path to prompts-10.json")
	cmd.Flags().BoolVar(&mock, "mock", true, "use the deterministic mock mode (no HTTP)")
	cmd.Flags().StringVar(&hostOverride, "host-override", "", "tailscale DNS name to substitute for 127.0.0.1 in the picked base_url")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print eval-smoke version",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = cmd, args
			fmt.Fprintf(cmd.OutOrStdout(), "eval-smoke %s (commit %s)\n", evalVersion, evalCommit)
		},
	}
}

// runMock is the v14510 mock-mode evaluator; it is exported inside
// the package so the test file can drive it directly.
func runMock(m *qwen36.Matrix, prompts []smoke.Prompt) []smoke.Result {
	results := make([]smoke.Result, 0, len(prompts))
	for _, p := range prompts {
		tier := qwen36.Tier(p.Tier)
		cell, err := qwen36.Pick(m, tier)
		result := smoke.Result{ID: p.ID, Tier: p.Tier}
		if err != nil {
			result.Passed = false
			result.Reason = "no_ready_cell"
			result.CellID = "none"
			results = append(results, result)
			continue
		}
		result.CellID = cell.ID
		result.BaseURL = cell.BaseURL("")
		resp := mockResponseFor(p)
		result.Passed = p.Rubric.Accepts(resp)
		if !result.Passed {
			result.Reason = "rubric_mismatch"
		}
		results = append(results, result)
	}
	return results
}

// mockResponseFor mirrors the in-test fixture so the binary and
// the test runner produce identical output for the same prompt
// set. Strategy: keyed on the prompt's numeric slot (parsed from
// the first digit after the tier prefix). Even slot -> pass,
// odd slot -> fail. With 10 prompts we get 5 pass / 5 fail.
func mockResponseFor(p smoke.Prompt) string {
	slot := promptSlot(p.ID)
	if slot%2 == 0 {
		return goodResponseFor(p.ID)
	}
	return "deliberate-fail: this response is intentionally wrong"
}

// promptSlot extracts the integer slot from an id like
// "t0-03-summarise-12-words" -> 3. We parse the digits after the
// dash; this is robust to any prefix as long as the format is
// t<digit>-NN-<anything>.
func promptSlot(id string) int {
	parts := bytesIndex(id, '-')
	if len(parts) < 2 {
		return 0
	}
	n := 0
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// bytesIndex splits id on '-' and returns the non-empty parts.
// Implemented locally to avoid pulling bytes for one function.
func bytesIndex(id string, sep rune) []string {
	var out []string
	last := 0
	for i, r := range id {
		if r == sep {
			if i > last {
				out = append(out, id[last:i])
			}
			last = i + 1
		}
	}
	if last < len(id) {
		out = append(out, id[last:])
	}
	return out
}

// goodResponseFor is the per-prompt canned good response that
// satisfies the rubric defined in eval-harness/prompts-10.json.
func goodResponseFor(id string) string {
	switch id {
	case "t0-01-json-ok":
		return `{"ok":true}`
	case "t0-02-extract-email":
		return `jason@helixon.io`
	case "t0-03-summarise-12-words":
		return "qwen3.6 matrix lists 7 cells across the fleet."
	case "t0-04-list-3-pitfalls":
		return `["loop var capture","nil interface","goroutine leak"]`
	case "t1-05-plan-migration":
		return "helm v2 to v3 plan:\nstep one: install helm 3\nstep two: convert tiller manifests\nstep three: validate charts\nstep four: cut over\nstep five: remove tiller"
	case "t1-06-score-prompt":
		return "7"
	case "t1-07-cheapest-tier0-cell":
		return "C1"
	case "t2-08-go-function":
		return "func larger(a, b int) int { if a > b { return a }; return b }"
	case "t3-09-audit-off-by-one":
		return "off-by-one: should be i<n not i<=n"
	case "t3-10-justify-q8-mtp":
		return "we use Q8_0 MTP for tier3 reasoning so we get speculative-decoding throughput on the dual RTX 3090."
	}
	return ""
}

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, qwen36.ErrNoReadyCell) {
		return 2
	}
	return 3
}

func defaultMatrixPath() string {
	if p := os.Getenv("QWEN36_MATRIX"); p != "" {
		return p
	}
	for _, root := range []string{
		"/home/jaslian/Code/cursor-global-kb",
		"/home/jaslian/Code/helixon-platform",
		"/mnt/c/Users/jaslian.DESKTOP-12RO1AF/Code/cursor-global-kb",
	} {
		candidate := root + "/scripts/fleet/qwen36-matrix.yaml"
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/home/jaslian/Code/cursor-global-kb/scripts/fleet/qwen36-matrix.yaml"
}

func defaultPromptsPath() string {
	if p := os.Getenv("EVAL_HARNESS_PROMPTS"); p != "" {
		return p
	}
	for _, root := range []string{
		"/home/jaslian/Code/helixon-platform",
		"/mnt/c/Users/jaslian.DESKTOP-12RO1AF/Code/helixon-platform",
	} {
		candidate := root + "/eval-harness/prompts-10.json"
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/home/jaslian/Code/helixon-platform/eval-harness/prompts-10.json"
}
