// Command helixon-eval is the v16129 Sprint 18 HelixonEval R3 binary.
//
// It exposes four subcommands:
//
//	helixon-eval run           -- score one or all golden tasks
//	helixon-eval report        -- render the aggregate report to stdout (or --out)
//	helixon-eval list-tasks    -- print the 5-task golden set
//	helixon-eval version       -- print version and exit
//
// Sprint 18 ships STAGING EVAL ONLY. Aliyun quota is exhausted, so the
// runner consumes synthesised offline traces from
// internal/helixon-eval.NewSynthSource instead of calling
// qwen3.7-plus/qwen3.7-max/MiniMax-M3 over HTTP. The sprint plan
// carries CARRY-056 — operator gates the public helixon-eval repo
// creation, so this binary currently lives inside helixon-platform.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	helixoneval "github.com/nfsarch33/helixon-platform/internal/helixon-eval"
)

var (
	version = "v16129.0"
	commit  = "dev"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "helixon-eval",
		Short: "HelixonEval R3 — agent task completion scoring across judge models",
		Long: `helixon-eval benchmarks Helixon platform/fleet agent task completion
on the supported judge models, applies the G-Eval rubrics, and emits an
aggregate report.

Sprint 18 runs in OFFLINE/STAGING mode: synthesised traces only. The
next sprint will plumb live API calls when Aliyun quota is restored.`,
		SilenceUsage: true,
	}
	root.AddCommand(
		newRunCmd(),
		newReportCmd(),
		newListTasksCmd(),
		newVersionCmd(),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print helixon-eval version",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd
			_ = args
			fmt.Printf("helixon-eval %s (commit %s)\n", version, commit)
		},
	}
}

func newListTasksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-tasks",
		Short: "print the 5-task golden set used by the regression harness",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd
			_ = args
			for i, t := range helixoneval.GoldenTasks() {
				fmt.Printf("%d. %s\n", i+1, t)
			}
		},
	}
}

func newRunCmd() *cobra.Command {
	var (
		task      string
		runAll    bool
		models    []string
		asJSON    bool
		threshold float64
		source    string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "execute the runner on one task or the entire golden set",
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, err := buildSource(source, time.Now().UTC())
			if err != nil {
				return err
			}
			reg := helixoneval.NewRegistry()
			runner := helixoneval.NewRunner(reg, src)
			mdl := parseModels(models)
			if runAll {
				n, err := runner.RunAll(mdl, helixoneval.GoldenCatalog())
				if err != nil {
					return err
				}
				if asJSON {
					return writeCasesJSON(cmd.OutOrStdout(), reg)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "ran %d cases (%d tasks × %d models) [source=%s]\n",
					n, len(helixoneval.GoldenTasks()), len(mdl), source)
				return nil
			}
			if task == "" {
				return fmt.Errorf("--task is required unless --all is set")
			}
			ids, err := runner.Run(task, mdl)
			if err != nil {
				return err
			}
			if asJSON {
				return writeCasesJSON(cmd.OutOrStdout(), reg)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ran %s on %d models: %s [source=%s]\n",
				task, len(ids), strings.Join(ids, ", "), source)
			// Suppress unused warning; threshold is honoured by `report`.
			_ = threshold
			return nil
		},
	}
	cmd.Flags().StringVar(&task, "task", "", "task ID (one of the golden set)")
	cmd.Flags().BoolVar(&runAll, "all", false, "run all golden tasks")
	cmd.Flags().StringSliceVar(&models, "models", defaultModels(),
		"comma-separated list of model identifiers (qwen3.7-plus|qwen3.7-max|MiniMax-M3|offline-fixture)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout instead of text")
	cmd.Flags().Float64Var(&threshold, "threshold", 0.7,
		"pass-threshold for the generated report (default 0.7)")
	cmd.Flags().StringVar(&source, "source", "synth",
		"trace source: synth (offline) or live (OpenAI-compatible upstreams)")
	return cmd
}

// buildSource wires the TraceSource for the CLI's run/report commands.
func buildSource(kind string, now time.Time) (helixoneval.TraceSource, error) {
	switch kind {
	case "synth", "":
		return helixoneval.NewSynthSource(now), nil
	case "live":
		return helixoneval.NewLiveSourceFromEnv(helixoneval.DefaultLiveEndpoints(), now), nil
	default:
		return nil, fmt.Errorf("unknown --source=%q (want synth or live)", kind)
	}
}

func newReportCmd() *cobra.Command {
	var (
		task      string
		runAll    bool
		models    []string
		outFile   string
		threshold = 0.7
		asJSON    bool
		source    string
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "run the golden set and emit the aggregate report (Markdown or JSON)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, err := buildSource(source, time.Now().UTC())
			if err != nil {
				return err
			}
			reg := helixoneval.NewRegistry()
			runner := helixoneval.NewRunner(reg, src)
			mdl := parseModels(models)
			if runAll {
				if _, err := runner.RunAll(mdl, helixoneval.GoldenCatalog()); err != nil {
					return err
				}
			} else {
				if task == "" {
					task = helixoneval.GoldenTasks()[0]
				}
				if _, err := runner.Run(task, mdl); err != nil {
					return err
				}
			}
			rep := helixoneval.Report{}
			rep.Aggregate(reg, "v18101", threshold)
			w := cmd.OutOrStdout()
			if outFile != "" {
				f, err := os.Create(outFile)
				if err != nil {
					return err
				}
				defer f.Close()
				w = f
			}
			if asJSON {
				return json.NewEncoder(w).Encode(rep)
			}
			return rep.WriteText(w)
		},
	}
	cmd.Flags().StringVar(&task, "task", "", "task ID (default: first golden task)")
	cmd.Flags().BoolVar(&runAll, "all", false, "run all golden tasks (default true in CI)")
	cmd.Flags().StringSliceVar(&models, "models", defaultModels(),
		"comma-separated list of model identifiers")
	cmd.Flags().StringVar(&outFile, "out", "", "write report to file (default: stdout)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of Markdown")
	cmd.Flags().Float64Var(&threshold, "threshold", 0.7,
		"pass-threshold for the report (default 0.7)")
	cmd.Flags().StringVar(&source, "source", "synth",
		"trace source: synth (offline) or live (OpenAI-compatible upstreams)")
	return cmd
}

func defaultModels() []string {
	return []string{"qwen3.7-plus", "qwen3.7-max", "MiniMax-M3"}
}

func parseModels(raw []string) []helixoneval.Model {
	out := make([]helixoneval.Model, 0, len(raw))
	for _, r := range raw {
		switch strings.TrimSpace(r) {
		case "qwen3.7-plus":
			out = append(out, helixoneval.ModelQwen37Plus)
		case "qwen3.7-max":
			out = append(out, helixoneval.ModelQwen37Max)
		case "MiniMax-M3":
			out = append(out, helixoneval.ModelMiniMaxM3)
		case "offline-fixture":
			out = append(out, helixoneval.ModelOfflineFix)
		default:
			fmt.Fprintf(os.Stderr, "helixon-eval: unknown model %q (ignored)\n", r)
		}
	}
	if len(out) == 0 {
		out = append(out, helixoneval.ModelOfflineFix)
	}
	return out
}

func writeCasesJSON(w io.Writer, reg *helixoneval.Registry) error {
	ids := reg.IDs()
	enc := json.NewEncoder(w)
	for _, id := range ids {
		c, ok := reg.Get(id)
		if !ok {
			continue
		}
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	return nil
}
