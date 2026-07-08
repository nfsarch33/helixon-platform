package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
)

// taskArgs collects the user-facing flags for the task subcommand.
type taskArgs struct {
	configPath string
	ticketID   string
	prompt     string
	engramURL  string
}

// taskDeps groups injectable dependencies used by the runTaskPipeline
// orchestrator. None are set in the production path; they exist so the
// pipeline function can be unit-tested with stubs.
type taskDeps struct {
	// Reserved for future injection (e.g., fake SprintboardClient).
}

// runTaskPipeline is the post-flag-parsing orchestrator for the task
// subcommand. v16716-3 refactor: replaces the inline RunE closure (CC=21)
// with five single-purpose helpers + a thin dispatcher (CC=3).
func runTaskPipeline(ctx context.Context, args taskArgs, _ taskDeps, out io.Writer) error {
	if args.ticketID == "" && args.prompt == "" {
		return fmt.Errorf("either --ticket or --prompt is required")
	}
	rt, cfg, err := setupTaskRuntime(ctx, args.configPath)
	if err != nil {
		return err
	}
	if rt == nil {
		return fmt.Errorf("task requires a configured LLM provider (kind != none)")
	}
	sbClient := buildSprintboardClient(cfg.SprintboardURL, cfg.AgentID)
	if sbClient != nil && args.ticketID != "" {
		_ = sbClient.ClaimTicket(ctx, args.ticketID)
	}
	taskPrompt := claimAndBuildPrompt(args.ticketID, args.prompt)
	fmt.Fprintf(out, "helixon task: agent_id=%q tools=%d\n", cfg.AgentID, rt.RegisteredToolCount())
	fmt.Fprintf(out, "helixon task: prompt=%q\n", truncate(taskPrompt, 120))
	resp, err := executeAndReport(ctx, rt, taskPrompt, args.ticketID, sbClient, out)
	if err != nil {
		return err
	}
	if args.engramURL != "" {
		doEngramPersist(ctx, out, args.engramURL, cfg.AgentID, taskPrompt, resp)
	}
	return nil
}

// setupTaskRuntime loads the config, builds the LLM provider, and
// initialises the helixon runtime with builtin tools registered.
// Returns (runtime, config, error). Runtime may be nil if the config has
// provider.kind=none (caller treats this as a "no-op" task).
func setupTaskRuntime(ctx context.Context, configPath string) (*helixon.Runtime, helixon.RuntimeConfig, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, helixon.RuntimeConfig{}, err
	}
	provider, err := helixon.BuildProvider(cfg.Provider)
	if err != nil {
		return nil, helixon.RuntimeConfig{}, fmt.Errorf("build provider: %w", err)
	}
	if provider == nil {
		return nil, cfg, nil
	}
	rt := helixon.NewRuntime(provider, cfg)
	if err := rt.Init(ctx); err != nil {
		return nil, helixon.RuntimeConfig{}, fmt.Errorf("runtime init: %w", err)
	}
	if err := builtins.RegisterAll(rt.Registry(), builtins.Options{
		Shell:     &builtins.ShellConfig{},
		FileRead:  &builtins.FileReadConfig{},
		FileWrite: &builtins.FileWriteConfig{},
	}); err != nil {
		return nil, helixon.RuntimeConfig{}, fmt.Errorf("register builtins: %w", err)
	}
	if err := rt.Configure(ctx); err != nil {
		return nil, helixon.RuntimeConfig{}, fmt.Errorf("runtime configure: %w", err)
	}
	return rt, cfg, nil
}

// claimAndBuildPrompt returns the prompt to feed to the agent. If the
// caller provided an explicit --prompt, it is returned unchanged.
// Otherwise a default prompt is constructed that names the ticket ID.
func claimAndBuildPrompt(ticketID, prompt string) string {
	if prompt != "" {
		return prompt
	}
	return fmt.Sprintf("Execute SprintBoard ticket %s. Investigate the task, use available tools to complete it, and report your findings.", ticketID)
}

// buildSprintboardClient returns a configured SprintboardClient when
// sprintboardURL is non-empty, otherwise nil. Returns nil when the
// configured URL is empty (the legacy "no Sprintboard" mode).
func buildSprintboardClient(sprintboardURL, agentID string) *controlplane.SprintboardClient {
	if sprintboardURL == "" {
		return nil
	}
	return controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   sprintboardURL,
		AgentName: agentID,
	}, slog.Default())
}

// executeAndReport runs the agent on the task prompt and, when a ticket
// was claimed, attempts to mark the ticket complete with the response
// (truncated) as evidence. Failures to mark complete are non-fatal.
func executeAndReport(ctx context.Context, rt *helixon.Runtime, taskPrompt, ticketID string, sbClient *controlplane.SprintboardClient, out io.Writer) (string, error) {
	resp, err := rt.HandleMessage(ctx, helixon.IncomingMessage{
		Channel: "task",
		Content: taskPrompt,
	})
	if err != nil {
		if ticketID != "" && sbClient != nil {
			_ = sbClient.CompleteTicket(ctx, ticketID, fmt.Sprintf("error: %v", err))
		}
		return "", fmt.Errorf("agent run: %w", err)
	}
	fmt.Fprintf(out, "\n--- Result ---\n%s\n", resp)
	if ticketID != "" && sbClient != nil {
		evidence := truncate(resp, 500)
		if err := sbClient.CompleteTicket(ctx, ticketID, evidence); err != nil {
			fmt.Fprintf(out, "warning: could not complete ticket %s: %v\n", ticketID, err)
		} else {
			fmt.Fprintf(out, "helixon task: ticket %s completed\n", ticketID)
		}
	}
	return resp, nil
}

func newTaskCmd() *cobra.Command {
	var args taskArgs
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Execute a SprintBoard ticket using the agent loop",
		Long: `Claim a SprintBoard ticket, execute it through the Helixon agent
loop with tool dispatch, persist conversation summary to Engram,
and report completion back to SprintBoard.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runTaskPipeline(ctx, args, taskDeps{}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&args.configPath, "config", "c", "", "Path to YAML config")
	cmd.Flags().StringVar(&args.ticketID, "ticket", "", "SprintBoard ticket ID to execute")
	cmd.Flags().StringVar(&args.prompt, "prompt", "", "Direct prompt (alternative to --ticket)")
	cmd.Flags().StringVar(&args.engramURL, "engram-url", os.Getenv("ENGRAM_URL"), "Engram server URL for conversation persistence")
	return cmd
}

func doEngramPersist(ctx context.Context, out io.Writer, engramURL, agentID, prompt, result string) {
	engram := memory.NewEngramClient(memory.EngramConfig{
		BaseURL: engramURL,
	}, slog.Default())

	summary := fmt.Sprintf("Agent %s executed task. Prompt: %s. Result: %s",
		agentID, truncate(prompt, 200), truncate(result, 300))

	mem, err := engram.Add(ctx, summary, "helixon", agentID)
	if err != nil {
		fmt.Fprintf(out, "warning: engram persistence failed: %v\n", err)
		return
	}
	if mem != nil {
		fmt.Fprintf(out, "helixon task: conversation persisted to engram (id=%s)\n", mem.ID)
	}
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
