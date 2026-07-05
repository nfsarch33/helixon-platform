// Command helixon is the operator entry point for the Helixon agent
// runtime. It exposes three subcommands:
//
//	helixon serve   -- start the runtime against a YAML config
//	helixon doctor  -- validate config and report runtime health
//	helixon repl    -- interactive single-turn loop for smoke tests
//
// The binary is intentionally thin: every behaviour lives in
// internal/helixon so the same code paths are exercised by tests,
// helixon-fleet, and the live runtime.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	_ "modernc.org/sqlite"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/dashboard"
	"github.com/nfsarch33/helixon-platform/internal/helixon/platform"
)

// runtimeView adapts *helixon.Runtime to dashboard.RuntimeView (Phase()
// returns the typed Phase; the dashboard expects a string).
type runtimeView struct{ rt *helixon.Runtime }

func (v runtimeView) AgentID() string               { return v.rt.AgentID() }
func (v runtimeView) Phase() string                 { return v.rt.PhaseString() }
func (v runtimeView) HeartbeatEvery() time.Duration { return v.rt.HeartbeatEvery() }
func (v runtimeView) ChannelCount() int             { return v.rt.ChannelCount() }
func (v runtimeView) RegisteredToolCount() int      { return v.rt.RegisteredToolCount() }

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "helixon",
		Short: "Helixon agent runtime CLI",
		Long: `helixon manages a Helixon agent runtime: validate config (doctor),
run the lifecycle (serve), and exercise tool dispatch interactively (repl).`,
		SilenceUsage: true,
	}
	root.AddCommand(newServeCmd(), newDoctorCmd(), newReplCmd(), newVersionCmd(), newPlatformCmd(), newTaskCmd())
	return root
}

// newPlatformCmd implements v8900-B13: `helixon platform` boots the
// platform HTTP/SSE server on :8787 (override with --addr or
// HELIXON_PORT). It uses an echo handler when no provider is wired so
// operators can sanity-check the bind without configuring an LLM.
func newPlatformCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "platform",
		Short: "Run the Helixon platform HTTP/SSE server (default :8787)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			bindAddr := resolvePlatformAddr(addr)
			handler := func(_ context.Context, msg helixon.IncomingMessage) (string, error) {
				return "echo:" + msg.Content, nil
			}
			srv := platform.FromHandler(handler, platform.Config{Addr: bindAddr})
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			fmt.Fprintf(cmd.OutOrStdout(), "helixon platform: listening on %s\n", bindAddr)
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "Bind address (default 127.0.0.1:8787 or $HELIXON_PORT)")
	return cmd
}

// resolvePlatformAddr returns the bind address for `helixon platform`.
// Precedence: --addr flag, then $HELIXON_PORT (with ":" preserved as-is
// or wrapped in 127.0.0.1:), then platform.DefaultAddr.
// Extracted to make the resolution testable without running a listener.
func resolvePlatformAddr(addrFlag string) string {
	if addrFlag != "" {
		return addrFlag
	}
	if env := os.Getenv("HELIXON_PORT"); env != "" {
		if strings.Contains(env, ":") {
			return env
		}
		return "127.0.0.1:" + env
	}
	return platform.DefaultAddr
}

func newServeCmd() *cobra.Command {
	var configPath string
	var heartbeat string
	var dashboardAddr string
	var httpAddr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Helixon runtime",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			if heartbeat != "" {
				d, err := time.ParseDuration(heartbeat)
				if err != nil {
					return fmt.Errorf("invalid --heartbeat: %w", err)
				}
				cfg.HeartbeatEvery = d
			}
			provider, err := helixon.BuildProvider(cfg.Provider)
			if err != nil {
				return fmt.Errorf("build provider: %w", err)
			}
			rt := helixon.NewRuntime(provider, cfg)
			if cfg.SprintboardURL != "" {
				sbClient := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
					BaseURL:      cfg.SprintboardURL,
					AgentName:    cfg.AgentID,
					Capabilities: cfg.SprintboardCapabilities,
				}, slog.Default())
				if err := helixon.WithSprintboard(sbClient)(rt); err != nil {
					return fmt.Errorf("wire sprintboard: %w", err)
				}
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			if err := rt.Init(ctx); err != nil {
				return fmt.Errorf("runtime init: %w", err)
			}

			if err := builtins.RegisterAll(rt.Registry(), builtins.Options{
				Shell:     &builtins.ShellConfig{},
				FileRead:  &builtins.FileReadConfig{},
				FileWrite: &builtins.FileWriteConfig{},
				WebFetch:  &builtins.WebFetchConfig{},
			}); err != nil {
				return fmt.Errorf("register builtins: %w", err)
			}

			configOpts := []helixon.ConfigOption{}
			if httpAddr != "" {
				configOpts = append(configOpts, helixon.WithChannel(
					helixon.NewHTTPChannel(helixon.HTTPChannelConfig{
						Addr:   httpAddr,
						Logger: slog.Default(),
					}),
				))
			}
			if err := rt.Configure(ctx, configOpts...); err != nil {
				return fmt.Errorf("runtime configure: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "helixon: agent_id=%q phase=%s heartbeat_every=%s tools=%d\n",
				cfg.AgentID, rt.Phase(), cfg.HeartbeatEvery, rt.RegisteredToolCount())
			if httpAddr != "" {
				fmt.Fprintf(out, "helixon: HTTP channel on %s (POST /api/v1/chat, GET /api/v1/health)\n", httpAddr)
			}

			var dashSrv *http.Server
			if dashboardAddr != "" {
				mux := http.NewServeMux()
				dashboard.Mount(mux, runtimeView{rt: rt})
				dashSrv = &http.Server{Addr: dashboardAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
				go func() {
					if err := dashSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
						fmt.Fprintf(os.Stderr, "dashboard server: %v\n", err)
					}
				}()
				fmt.Fprintf(out, "helixon: dashboard at http://%s/api/v1/dashboard\n", dashboardAddr)
			}

			runErr := rt.Run(ctx)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			if dashSrv != nil {
				_ = dashSrv.Shutdown(shutdownCtx)
			}
			if err := rt.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("runtime shutdown: %w", err)
			}
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				return fmt.Errorf("runtime run: %w", runErr)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config (required)")
	cmd.Flags().StringVar(&heartbeat, "heartbeat", "", "Override heartbeat_every (e.g. 30s)")
	cmd.Flags().StringVar(&dashboardAddr, "dashboard-addr", "", "Bind /api/v1/dashboard on this address (e.g. 127.0.0.1:9410)")
	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "Bind HTTP channel (POST /api/v1/chat, GET /api/v1/health)")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func newDoctorCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate config and report runtime readiness",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "helixon doctor (version=%s commit=%s)\n", version, commit)
			if configPath == "" {
				fmt.Fprintln(out, "  config:        (none provided; pass --config to validate)")
				return nil
			}
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "  config path:   %s\n", configPath)
			fmt.Fprintf(out, "  agent_id:      %q\n", cfg.AgentID)
			fmt.Fprintf(out, "  session_dsn:   %q\n", cfg.SessionDSN)
			fmt.Fprintf(out, "  max_iter:      %d\n", cfg.MaxIterations)
			fmt.Fprintf(out, "  max_tokens:    %d\n", cfg.MaxTokens)
			fmt.Fprintf(out, "  timeout:       %s\n", cfg.Timeout)
			fmt.Fprintf(out, "  heartbeat:     %s\n", cfg.HeartbeatEvery)
			if cfg.Provider.Kind != "" {
				fmt.Fprintf(out, "  provider:      kind=%s base=%q model=%q timeout=%s\n",
					cfg.Provider.Kind, cfg.Provider.BaseURL, cfg.Provider.Model, cfg.Provider.Timeout)
			} else {
				fmt.Fprintln(out, "  provider:      (none)")
			}
			if _, err := helixon.BuildProvider(cfg.Provider); err != nil {
				fmt.Fprintf(out, "  provider check: ERROR %v\n", err)
			} else {
				fmt.Fprintln(out, "  provider check: ok")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config")
	return cmd
}

func newReplCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Interactive agent loop with tool dispatch (Ctrl-D to exit)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			provider, provErr := helixon.BuildProvider(cfg.Provider)
			if provErr != nil {
				return fmt.Errorf("build provider: %w", provErr)
			}
			rt := helixon.NewRuntime(provider, cfg)
			ctx := cmd.Context()
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
			if provider == nil {
				fmt.Fprintf(out, "helixon repl: agent_id=%q (no provider; echo mode) Ctrl-D to exit\n", cfg.AgentID)
				scanner := bufio.NewScanner(cmd.InOrStdin())
				for scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if line == "" {
						continue
					}
					if line == ":quit" || line == ":exit" {
						break
					}
					fmt.Fprintf(out, "echo: %s\n", line)
				}
				return scanner.Err()
			}

			fmt.Fprintf(out, "helixon repl: agent_id=%q tools=%d (Ctrl-D to exit)\n",
				cfg.AgentID, rt.RegisteredToolCount())
			scanner := bufio.NewScanner(cmd.InOrStdin())
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				if line == ":quit" || line == ":exit" {
					break
				}
				msg := helixon.IncomingMessage{
					Channel: "repl",
					Content: line,
				}
				resp, err := rt.HandleMessage(ctx, msg)
				if err != nil {
					fmt.Fprintf(out, "error: %v\n", err)
					continue
				}
				fmt.Fprintf(out, "%s\n", resp)
			}
			return scanner.Err()
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config (optional, defaults applied)")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print binary version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "helixon %s (%s)\n", version, commit)
			return nil
		},
	}
}

func loadConfig(path string) (helixon.RuntimeConfig, error) {
	if path == "" {
		// repl-style: zero config + defaults.
		return helixon.RuntimeConfig{Logger: slog.Default()}, nil
	}
	cfg, err := helixon.LoadConfig(path)
	if err != nil {
		return helixon.RuntimeConfig{}, err
	}
	cfg.Logger = slog.Default()
	return cfg, nil
}
