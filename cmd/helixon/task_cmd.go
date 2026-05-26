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

func newTaskCmd() *cobra.Command {
	var (
		configPath string
		ticketID   string
		prompt     string
		engramURL  string
	)
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Execute a SprintBoard ticket using the agent loop",
		Long: `Claim a SprintBoard ticket, execute it through the Helixon agent
loop with tool dispatch, persist conversation summary to Engram,
and report completion back to SprintBoard.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ticketID == "" && prompt == "" {
				return fmt.Errorf("either --ticket or --prompt is required")
			}

			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}

			provider, err := helixon.BuildProvider(cfg.Provider)
			if err != nil {
				return fmt.Errorf("build provider: %w", err)
			}
			if provider == nil {
				return fmt.Errorf("task requires a configured LLM provider (kind != none)")
			}

			rt := helixon.NewRuntime(provider, cfg)
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			if err := rt.Init(ctx); err != nil {
				return fmt.Errorf("runtime init: %w", err)
			}
			if err := builtins.RegisterAll(rt.Registry(), builtins.Options{
				Shell:     &builtins.ShellConfig{},
				FileRead:  &builtins.FileReadConfig{},
				FileWrite: &builtins.FileWriteConfig{},
			}); err != nil {
				return fmt.Errorf("register builtins: %w", err)
			}
			if err := rt.Configure(ctx); err != nil {
				return fmt.Errorf("runtime configure: %w", err)
			}

			out := cmd.OutOrStdout()

			var sbClient *controlplane.SprintboardClient
			if cfg.SprintboardURL != "" {
				sbClient = controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
					BaseURL:   cfg.SprintboardURL,
					AgentName: cfg.AgentID,
				}, slog.Default())
			}

			taskPrompt := prompt
			if ticketID != "" {
				if sbClient != nil {
					if err := sbClient.ClaimTicket(ctx, ticketID); err != nil {
						fmt.Fprintf(out, "warning: could not claim ticket %s: %v\n", ticketID, err)
					} else {
						fmt.Fprintf(out, "helixon task: claimed ticket %s\n", ticketID)
					}
				}
				if taskPrompt == "" {
					taskPrompt = fmt.Sprintf("Execute SprintBoard ticket %s. Investigate the task, use available tools to complete it, and report your findings.", ticketID)
				}
			}

			fmt.Fprintf(out, "helixon task: agent_id=%q tools=%d\n", cfg.AgentID, rt.RegisteredToolCount())
			fmt.Fprintf(out, "helixon task: prompt=%q\n", truncate(taskPrompt, 120))

			msg := helixon.IncomingMessage{
				Channel: "task",
				Content: taskPrompt,
			}
			resp, err := rt.HandleMessage(ctx, msg)
			if err != nil {
				if ticketID != "" && sbClient != nil {
					evidence := fmt.Sprintf("error: %v", err)
					_ = sbClient.CompleteTicket(ctx, ticketID, evidence)
				}
				return fmt.Errorf("agent run: %w", err)
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

			if engramURL != "" {
				doEngramPersist(ctx, out, engramURL, cfg.AgentID, taskPrompt, resp)
			}

			return nil
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config")
	cmd.Flags().StringVar(&ticketID, "ticket", "", "SprintBoard ticket ID to execute")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Direct prompt (alternative to --ticket)")
	cmd.Flags().StringVar(&engramURL, "engram-url", os.Getenv("ENGRAM_URL"), "Engram server URL for conversation persistence")
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
