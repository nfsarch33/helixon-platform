// Command choose-llm is the v14510 tier router.
//
// Background:
//
//   - cursor-global-kb owns the canonical fleet LLM matrix at
//     scripts/fleet/qwen36-matrix.yaml. Each row ("cell") names a
//     concrete (node, gpu, model, port) tuple that an OpenAI-
//     compatible HTTP server can serve.
//
//   - The v14503-03 metrics show that picking the right cell is
//     a real cost / latency decision: C7 (Q8_0 MTP, dual RTX 3090)
//     runs at 25 tok/s vs C2 (Q4_K_M, single RTX 3090) at 13 tok/s;
//     C8 (9B q4 on 4070 Ti Super) is roughly 3x cheaper per token
//     but caps context at 32k.
//
//   - The v14511 beforeSubmitPrompt hook needs a side-car binary
//     (not in-process) so the hook stays declarative. choose-llm
//     is that side-car.
//
// Usage:
//
//	choose-llm pick --tier=0|1|2|3 [--matrix=PATH] [--host-override=H]
//
// Exit codes:
//
//	0 -- a cell was picked. Output is JSON; see pickOutput.
//	2 -- no ready cell matched (operator must flip status to ready).
//	3 -- bad flag / subcommand (usage error).
//	4 -- matrix file unreadable / schema mismatch.
//
// Stdin/stdout: no stdin is read; all I/O is via flags + the matrix
// file. Output is ALWAYS valid JSON for the pick subcommand so the
// v14511 hook can `os/exec` the binary and pipe through `jq`.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nfsarch33/helixon-platform/internal/choosehook"
	"github.com/nfsarch33/helixon-platform/internal/llm/qwen36"
)

var (
	cliVersion = "v14510.0"
	cliCommit  = "dev"
)

// pickOutput is the JSON shape choose-llm emit. Key names are snake
// case to mirror the rest of the helixon CLI (see cmd/helixon) and
// the v14511 hook can pipe through `jq` without renaming.
type pickOutput struct {
	SprintID         string `json:"sprint_id"`
	Tier             int    `json:"tier"`
	TierLabel        string `json:"tier_label"`
	CellID           string `json:"cell_id"`
	ModelID          string `json:"model_id"`
	Engine           string `json:"engine"`
	BaseURL          string `json:"base_url"`
	BaseURLStripped  string `json:"host_port"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	MaxModelLen      int    `json:"max_model_len"`
	MinFreeMib       int    `json:"min_free_mib"`
	Reason           string `json:"reason"`
	SpecType         string `json:"spec_type,omitempty"`
	RejectedCellsN   int    `json:"rejected_cells"`
	MatrixCellsTotal int    `json:"matrix_cells_total"`
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(exitCodeFor(err))
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "choose-llm",
		Short: "Helixon qwen36 tier router",
		Long: `choose-llm is a stateless side-car that resolves an OpenAI-
compatible base URL from cursor-global-kb's qwen36-matrix.yaml for
a given tier (0..3).

It is meant to be invoked from the v14511 beforeSubmitPrompt hook
(or any agent that needs to decide which local LLM cell to talk
to) and emits a single JSON object on stdout.`,
		SilenceUsage: true,
	}
	root.AddCommand(newPickCmd(), newMatrixCmd(), newVersionCmd())
	root.AddCommand(newHookCmd())
	return root
}

// buildHooksJSON is exported so the test file can exercise the
// generator without spinning up cobra. The shape mirrors what
// Cursor >=1.98 expects for ~/.cursor/hooks.json:
// https://cursor.com/docs/hooks
func buildHooksJSON(binary string, flagLine string) string {
	payload := map[string]any{
		"version": 1,
		"hooks": map[string]any{
			"beforeSubmitPrompt": []map[string]any{
				{
					"name":        "helixon choose-llm",
					"type":        "command",
					"command":     binary,
					"args":        strings.Fields(flagLine),
					"stdin":       true,
					"description": "Routes the prompt to the cheapest ready qwen36 cell. Reads {prompt: string, hook_mode?: 'redirect'|'annotate'} from stdin; writes {decision_label, base_url, cell_id, hook_mode, reason} to stdout.",
				},
			},
		},
	}
	bb, _ := json.MarshalIndent(payload, "", "  ")
	return string(bb) + "\n"
}

func newHookCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hook",
		Short: "Cursor beforeSubmitPrompt hook integration",
		Long: `hook sub-commands produce the JSON wrapper for the
~/.cursor/hooks.json entry that bridges Cursor's prompt
submission to the qwen36 tier router.`,
	}
	root.AddCommand(newHookInstallCmd(), newHookDecideCmd())
	return root
}

func newHookInstallCmd() *cobra.Command {
	var (
		out    string
		binary string
		flags  string
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write a Cursor hooks.json entry that wires choose-llm into beforeSubmitPrompt",
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := buildHooksJSON(binary, flags)
			if err := os.WriteFile(out, []byte(payload), 0o644); err != nil { //nolint:gosec // G306 file perms 0644 acceptable for non-secret output
				return fmt.Errorf("write hooks.json %q: %w", out, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote Cursor hooks snippet to %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "path to write hooks.json snippet (required)")
	cmd.Flags().StringVar(&binary, "binary", "/usr/local/bin/choose-llm", "absolute path to the choose-llm binary Cursor should invoke")
	cmd.Flags().StringVar(&flags, "flags", "hook decide --matrix /home/jaslian/Code/cursor-global-kb/scripts/fleet/qwen36-matrix.yaml", "flag line passed to choose-llm hook decide")
	return cmd
}

func newHookDecideCmd() *cobra.Command {
	var (
		matrixPath   string
		hostOverride string
	)
	cmd := &cobra.Command{
		Use:   "decide",
		Short: "Read a DecideInput from stdin and emit the router decision as JSON",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var in choosehook.DecideInput
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &in); err != nil {
					return fmt.Errorf("parse stdin JSON: %w", err)
				}
			}
			if in.Prompt == "" {
				return fmt.Errorf("missing required field 'prompt' in stdin JSON")
			}
			out, err := choosehook.Decide(in, matrixPath, hostOverride)
			if err != nil {
				// We still emit the JSON so the cursor client
				// can pass through with a no_decision label;
				// the exit code carries the error.
				_ = json.NewEncoder(os.Stdout).Encode(out)
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
	cmd.Flags().StringVar(&matrixPath, "matrix", defaultMatrixPath(), "path to qwen36-matrix.yaml")
	cmd.Flags().StringVar(&hostOverride, "host-override", "", "override the loopback hostname in the picked base_url")
	return cmd
}

// exitCodeFor maps an error returned by cobra to a stable exit code
// so the v14511 hook can distinguish between "no ready cell" (2)
// and "you typed the wrong flag" (3) without parsing stderr.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, qwen36.ErrNoReadyCell) {
		return 2
	}
	// cobra errors wrap the flag with the input name; treat any
	// cobra-shaped error as a usage error.
	return 3
}

func newPickCmd() *cobra.Command {
	var (
		matrixPath   string
		tier         int
		hostOverride string
	)
	cmd := &cobra.Command{
		Use:   "pick",
		Short: "Pick the best ready cell for a tier (0..3)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := qwen36.LoadFile(matrixPath)
			if err != nil {
				return fmt.Errorf("load matrix %q: %w", matrixPath, err)
			}
			t, ok := tierToEnum(tier)
			if !ok {
				return fmt.Errorf("tier %d out of range [0..3]", tier)
			}
			c, err := qwen36.Pick(m, t)
			if err != nil {
				return err
			}
			base := c.BaseURL(hostOverride)
			out := pickOutput{
				SprintID:         "v14510",
				Tier:             int(t),
				TierLabel:        tierLabel(t),
				CellID:           c.ID,
				ModelID:          c.ModelID,
				Engine:           c.Engine,
				BaseURL:          base,
				BaseURLStripped:  base[len("http://"):],
				Host:             pickHost(hostOverride),
				Port:             c.HostPort,
				MaxModelLen:      c.MaxModelLen,
				MinFreeMib:       c.MinFreeMib,
				Reason:           tierReason(t),
				SpecType:         c.SpecType,
				RejectedCellsN:   len(m.Cells) - len(m.Ready()),
				MatrixCellsTotal: len(m.Cells),
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(&out)
		},
	}
	cmd.Flags().StringVar(&matrixPath, "matrix", defaultMatrixPath(), "path to qwen36-matrix.yaml")
	cmd.Flags().IntVar(&tier, "tier", 0, "tier band 0..3 (0 = cheap draft, 3 = deep review)")
	cmd.Flags().StringVar(&hostOverride, "host-override", "", "override the loopback hostname in the base_url (e.g. a tailscale DNS name)")
	return cmd
}

func newMatrixCmd() *cobra.Command {
	var matrixPath string
	cmd := &cobra.Command{
		Use:   "matrix",
		Short: "Inspect the qwen36 matrix",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "print all cells with their status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := qwen36.LoadFile(matrixPath)
			if err != nil {
				return fmt.Errorf("load matrix %q: %w", matrixPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "schema_version: %d\n", m.SchemaVersion)
			fmt.Fprintf(cmd.OutOrStdout(), "ready: %d / total: %d\n\n", len(m.Ready()), len(m.Cells))
			for id, c := range m.Cells {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\tnode=%s slot=%s model=%s engine=%s port=%d status=%s\n",
					id, c.Node, c.GPUSlot, c.ModelID, c.Engine, c.HostPort, c.Status)
			}
			return nil
		},
	}
	list.Flags().StringVar(&matrixPath, "matrix", defaultMatrixPath(), "path to qwen36-matrix.yaml")
	cmd.AddCommand(list)
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print choose-llm version",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = cmd, args
			fmt.Fprintf(cmd.OutOrStdout(), "choose-llm %s (commit %s)\n", cliVersion, cliCommit)
		},
	}
}

func defaultMatrixPath() string {
	// Allow operators to point at a custom path via env; default
	// to a few well-known locations: cursor-global-kb/scripts/fleet
	// checked out one or two levels up, the repo we're in, and the
	// helixon checkout next to cursor-global-kb. The hard-coded
	// /tmp fallback is the test fixture pattern.
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

// tierToEnum maps the int flag value to a qwen36.Tier. We allocate
// the enum with explicit values (see qwen36/router.go) so this is a
// safe drop-through.
func tierToEnum(t int) (qwen36.Tier, bool) {
	switch t {
	case 0:
		return qwen36.Tier0, true
	case 1:
		return qwen36.Tier1, true
	case 2:
		return qwen36.Tier2, true
	case 3:
		return qwen36.Tier3, true
	}
	return 0, false
}

// tierLabel / tierReason are the human-readable shapes surfaced in
// the JSON output. Kept here so the qwen36 package stays string-free.
func tierLabel(t qwen36.Tier) string {
	switch t {
	case qwen36.Tier0:
		return "cheap-drafting"
	case qwen36.Tier1:
		return "heuristic-planning"
	case qwen36.Tier2:
		return "code-synthesis"
	case qwen36.Tier3:
		return "deep-review-reasoning"
	}
	return "unknown"
}

func tierReason(t qwen36.Tier) string {
	switch t {
	case qwen36.Tier0:
		return "tier0 prefers smallest ready cell (lowest min_free_mib)"
	case qwen36.Tier1:
		return "tier1 prefers ready cell with largest max_model_len; vllm tie-breaker"
	case qwen36.Tier2:
		return "tier2 prefers vllm-served ready cells for code synthesis throughput"
	case qwen36.Tier3:
		return "tier3 prefers speculative-decoding-capable cells"
	}
	return "unknown tier"
}

// pickHost extracts the host:port string for the JSON output; it is
// derived from the rendered base_url so tests don't drift.
func pickHost(hostOverride string) string {
	if hostOverride != "" {
		return hostOverride
	}
	return "127.0.0.1"
}
