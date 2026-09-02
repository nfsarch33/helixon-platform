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
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"

	_ "modernc.org/sqlite"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/dashboard"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
	"github.com/nfsarch33/helixon-platform/internal/helixon/platform"
	"github.com/nfsarch33/helixon-platform/internal/llm"
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
	root.AddCommand(newServeCmd(), newDoctorCmd(), newReplCmd(), newVersionCmd(), newPlatformCmd(), newTaskCmd(), newMemoryCmd())
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
			flags := serveFlags{configPath: configPath, heartbeat: heartbeat, dashboardAddr: dashboardAddr, httpAddr: httpAddr}
			return runServe(cmd, flags)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config (required)")
	cmd.Flags().StringVar(&heartbeat, "heartbeat", "", "Override heartbeat_every (e.g. 30s)")
	cmd.Flags().StringVar(&dashboardAddr, "dashboard-addr", "", "Bind /api/v1/dashboard on this address (e.g. 127.0.0.1:9410)")
	cmd.Flags().StringVar(&httpAddr, "http-addr", "", "Bind HTTP channel (POST /api/v1/chat, GET /api/v1/health)")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

// serveFlags captures the user-supplied flags for the serve subcommand.
type serveFlags struct {
	configPath    string
	heartbeat     string
	dashboardAddr string
	httpAddr      string
}

// runServe is the post-flag-parsing orchestrator for `helixon serve`. It
// composes the smaller helpers below; each one has a single responsibility.
func runServe(cmd *cobra.Command, f serveFlags) error {
	cfg, err := loadServeConfig(f.configPath, f.heartbeat)
	if err != nil {
		return err
	}
	rt, err := buildServeRuntime(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := initAndConfigureRuntime(ctx, rt, cfg, f.httpAddr); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	printServeBanner(out, cfg.AgentID, rt, f.httpAddr)
	dashSrv := startServeDashboard(rt, f.dashboardAddr, out)
	return runAndShutdown(ctx, rt, dashSrv)
}

// printServeBanner writes the standard "helixon: ..." startup banner.
func printServeBanner(out io.Writer, agentID string, rt *helixon.Runtime, httpAddr string) {
	fmt.Fprintf(out, "helixon: agent_id=%q phase=%s heartbeat_every=%s tools=%d\n",
		agentID, rt.Phase(), rt.HeartbeatEvery(), rt.RegisteredToolCount())
	if httpAddr != "" {
		fmt.Fprintf(out, "helixon: HTTP channel on %s (POST /api/v1/chat, GET /api/v1/health)\n", httpAddr)
	}
	_, _ = fmt.Fprint(out, ticketPollerBanner(rt.TicketPoller()))
}

// ticketPollerBanner renders the autonomy line. An agent that pulls its own
// work must say so on every start; silence is how "it only answers when
// spoken to" and "it claims and executes tickets unattended" become
// indistinguishable from the console.
func ticketPollerBanner(p *helixon.TicketPoller) string {
	if p == nil {
		return "helixon: ticket polling DISABLED (set tickets.enabled to let this agent pull its own work)\n"
	}
	c := p.Config()
	return fmt.Sprintf("helixon: ticket polling ENABLED status=%q interval=%s max_concurrent=%d ticket_timeout=%s\n",
		c.Status, c.Interval, c.MaxConcurrent, c.TicketTimeout)
}

func loadServeConfig(configPath, heartbeat string) (helixon.RuntimeConfig, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return helixon.RuntimeConfig{}, err
	}
	if heartbeat != "" {
		d, err := time.ParseDuration(heartbeat)
		if err != nil {
			return helixon.RuntimeConfig{}, fmt.Errorf("invalid --heartbeat: %w", err)
		}
		cfg.HeartbeatEvery = d
	}
	return cfg, nil
}

func buildServeRuntime(cfg helixon.RuntimeConfig) (*helixon.Runtime, error) {
	provider, err := helixon.BuildProvider(cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("build provider: %w", err)
	}
	rt := helixon.NewRuntime(provider, cfg)
	if cfg.SprintboardURL != "" {
		sbClient := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
			BaseURL:      cfg.SprintboardURL,
			AgentName:    cfg.AgentID,
			Capabilities: cfg.SprintboardCapabilities,
		}, slog.Default())
		if err := helixon.WithSprintboard(sbClient)(rt); err != nil {
			return nil, fmt.Errorf("wire sprintboard: %w", err)
		}
	}
	return rt, nil
}

//nolint:gocritic // hugeParam: RuntimeConfig is copied deliberately
func initAndConfigureRuntime(ctx context.Context, rt *helixon.Runtime, cfg helixon.RuntimeConfig, httpAddr string) error {
	if err := rt.Init(ctx); err != nil {
		return fmt.Errorf("runtime init: %w", err)
	}
	metrics, gatherer, err := newAgentObservability(cfg, commit)
	if err != nil {
		return err
	}
	g, err := newGuardrails(cfg)
	if err != nil {
		return err
	}
	g = g.withMetrics(metrics)
	if err := builtins.RegisterAll(rt.Registry(), g.toolOptions(true)); err != nil {
		return fmt.Errorf("register builtins: %w", err)
	}
	configOpts := g.configOptions()
	if am := buildAgentMemory(cfg); am != nil {
		configOpts = append(configOpts, helixon.WithAgentMemory(am))
	}
	if httpAddr != "" {
		configOpts = append(configOpts, helixon.WithChannel(newServeHTTPChannel(httpAddr, gatherer)))
	}
	if err := rt.Configure(ctx, configOpts...); err != nil {
		return fmt.Errorf("runtime configure: %w", err)
	}
	return nil
}

// newAgentObservability builds the agent's own Prometheus registry.
//
// A dedicated registry, not prometheus.DefaultRegisterer: the default one is
// process-global and carries Go runtime and process collectors, so a scrape of
// it succeeds — with plausible-looking series — from a binary in which the
// agent was never wired at all. The whole point of hlxn_agent_build_info is
// that its ABSENCE means "no agent here", and that only holds if nothing else
// can answer on the same endpoint.
//
// Returns (nil, nil, nil) when metrics are switched off. A nil Gatherer is what
// makes /metrics 404 rather than serve an empty page, so "disabled" and
// "running but reporting nothing" stay distinguishable from the outside.
//
//nolint:gocritic // hugeParam: see initAndConfigureRuntime
func newAgentObservability(cfg helixon.RuntimeConfig, revision string) (*agentmetrics.Metrics, prometheus.Gatherer, error) {
	if !cfg.Metrics.Enabled {
		return nil, nil, nil
	}
	reg := prometheus.NewRegistry()
	m, err := agentmetrics.New(reg, revision)
	if err != nil {
		return nil, nil, fmt.Errorf("agent metrics: %w", err)
	}
	return m, reg, nil
}

// newServeHTTPChannel builds the serve-mode HTTP channel. It is a named
// function so a test can assert what the serve path actually hands the channel
// — the wiring that was missing is exactly the kind an inline struct literal
// hides.
func newServeHTTPChannel(addr string, gatherer prometheus.Gatherer) *helixon.HTTPChannel {
	return helixon.NewHTTPChannel(helixon.HTTPChannelConfig{
		Addr:     addr,
		Logger:   slog.Default(),
		Gatherer: gatherer,
	})
}

// buildAgentMemory wires loop memory for the ticket path, or returns nil.
//
// The local FTS5 mirror is deliberately not wired here (a nil *sql.DB): the
// canonical store is Engram, HybridSearcher already treats a nil db as "no
// local index", and standing up a second durable store on the agent host is a
// separate decision from letting the agent remember anything at all.
//
//nolint:gocritic // hugeParam: see initAndConfigureRuntime
func buildAgentMemory(cfg helixon.RuntimeConfig) *memory.AgentMemory {
	mc := cfg.Memory
	if !mc.Active() {
		return nil
	}
	workspace := mc.WorkspaceID
	if workspace == "" {
		workspace = cfg.TenantID
	}
	client := memory.NewEngramClient(memory.EngramConfig{
		BaseURL: mc.EngramURL,
		Timeout: mc.Timeout,
	}, slog.Default())
	searcher := memory.NewHybridSearcher(nil, client, memory.HybridSearchConfig{
		MaxResults: mc.MaxContext,
	}, slog.Default())
	return memory.NewAgentMemory(searcher, memory.AgentMemoryConfig{
		AppID:      mc.AppID,
		UserID:     cfg.AgentID,
		TenantID:   workspace,
		MaxContext: mc.MaxContext,
		Logger:     slog.Default(),
	})
}

// resolveMemoryURL fills an unset memory.engram_url from $HELIXON_ENGRAM_URL.
//
// The env fallback lives at the CLI layer, not in the config decoder, for the
// same reason defaultMemoryBackend() already reads that variable here: a
// RuntimeConfig built in a test must depend on nothing but the bytes it was
// given. An explicit YAML value always wins.
//
//nolint:gocritic // hugeParam: value semantics; the resolved copy is returned
func resolveMemoryURL(mc helixon.LoopMemoryConfig, env string) helixon.LoopMemoryConfig {
	if mc.EngramURL == "" {
		mc.EngramURL = strings.TrimSpace(env)
	}
	return mc
}

func startServeDashboard(rt *helixon.Runtime, dashboardAddr string, out io.Writer) *http.Server {
	if dashboardAddr == "" {
		return nil
	}
	mux := http.NewServeMux()
	dashboard.Mount(mux, runtimeView{rt: rt})
	// The operator console's read API (v18809): runs, costs, evals, memory.
	// Locations come from the environment with conventional defaults; the
	// console renders absence as absence, so an unset path is not an error.
	ccfg := dashboard.DefaultConsoleConfig()
	if v := os.Getenv("HLXN_EVAL_LEDGER"); v != "" {
		ccfg.EvalLedgerPath = v
	}
	if v := os.Getenv("HLXN_TEXTFILE_DIR"); v != "" {
		ccfg.TextfileDir = v
	}
	var mem dashboard.MemorySearcher
	if m := rt.Memory(); m != nil {
		mem = m
	}
	dashboard.MountConsole(mux, rt.Store(), mem, &ccfg)
	srv := &http.Server{Addr: dashboardAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "dashboard server: %v\n", err)
		}
	}()
	fmt.Fprintf(out, "helixon: dashboard at http://%s/api/v1/dashboard\n", dashboardAddr)
	return srv
}

func runAndShutdown(ctx context.Context, rt *helixon.Runtime, dashSrv *http.Server) error {
	runErr := rt.Run(ctx)
	// Shutdown uses a fresh context because parent is cancelled by Run's return.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second) //nolint:contextcheck
	defer shutdownCancel()
	if dashSrv != nil {
		_ = dashSrv.Shutdown(shutdownCtx) //nolint:contextcheck
	}
	if err := rt.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) { //nolint:contextcheck
		return fmt.Errorf("runtime shutdown: %w", err)
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("runtime run: %w", runErr)
	}
	return nil
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
			rt, setupErr := setupReplRuntime(cfg, provider)
			if setupErr != nil {
				return setupErr
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			fmt.Fprint(out, replPrompt(cfg.AgentID, rt.RegisteredToolCount(), provider == nil))
			if provider == nil {
				return runReplEchoMode(out, cmd.InOrStdin())
			}
			return runReplDispatchMode(out, cmd.InOrStdin(), ctx, rt)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to YAML config (optional, defaults applied)")
	return cmd
}

// setupReplRuntime builds a runtime from the config + provider and registers
// the standard 3 builtin tools (Shell, FileRead, FileWrite). Extracted from
// newReplCmd.RunE so the helper is independently testable.
func setupReplRuntime(cfg helixon.RuntimeConfig, provider llm.Provider) (*helixon.Runtime, error) {
	rt := helixon.NewRuntime(provider, cfg)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		return nil, fmt.Errorf("runtime init: %w", err)
	}
	g, err := newGuardrails(cfg)
	if err != nil {
		return nil, err
	}
	if err := builtins.RegisterAll(rt.Registry(), g.toolOptions(false)); err != nil {
		return nil, fmt.Errorf("register builtins: %w", err)
	}
	if err := rt.Configure(ctx, g.configOptions()...); err != nil {
		return nil, fmt.Errorf("runtime configure: %w", err)
	}
	return rt, nil
}

// replPrompt renders the header line shown before the scanner loop. Centralised
// so tests assert exact text and the orchestrator does not branch on it.
func replPrompt(agentID string, toolCount int, echoMode bool) string {
	if echoMode {
		return fmt.Sprintf("helixon repl: agent_id=%q (no provider; echo mode) Ctrl-D to exit\n", agentID)
	}
	return fmt.Sprintf("helixon repl: agent_id=%q tools=%d (Ctrl-D to exit)\n", agentID, toolCount)
}

// runReplEchoMode is the no-provider branch: each line is echoed back verbatim.
// Stops on :quit/:exit; blank lines are skipped.
func runReplEchoMode(out io.Writer, in io.Reader) error {
	scanner := bufio.NewScanner(in)
	return runReplScannerLoop(out, scanner, func(line string) error {
		fmt.Fprintf(out, "echo: %s\n", line)
		return nil
	})
}

// runReplDispatchMode is the with-provider branch: each line is dispatched
// to the runtime via HandleMessage and the response is written to out.
// Stops on :quit/:exit; per-line errors are reported but do not abort the loop.
func runReplDispatchMode(out io.Writer, in io.Reader, ctx context.Context, rt *helixon.Runtime) error {
	scanner := bufio.NewScanner(in)
	return runReplScannerLoop(out, scanner, func(line string) error {
		msg := helixon.IncomingMessage{Channel: "repl", Content: line}
		resp, err := rt.HandleMessage(ctx, msg)
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return nil
		}
		fmt.Fprintf(out, "%s\n", resp)
		return nil
	})
}

// runReplScannerLoop is the shared line-driven loop. It returns scanner.Err()
// at the end. The handler is invoked once per non-blank, non-quit line.
func runReplScannerLoop(out io.Writer, scanner *bufio.Scanner, handler func(line string) error) error {
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == ":quit" || line == ":exit" {
			break
		}
		if err := handler(line); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
		}
	}
	return scanner.Err()
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
		// repl-style: no file, but the SAME guardrail defaults an empty
		// YAML document would produce. Returning a bare zero value here
		// would quietly hand the config-less path a disabled sandbox.
		cfg, err := helixon.DefaultRuntimeConfig()
		if err != nil {
			return helixon.RuntimeConfig{}, err
		}
		cfg.Logger = slog.Default()
		cfg.Memory = resolveMemoryURL(cfg.Memory, os.Getenv("HELIXON_ENGRAM_URL"))
		return cfg, nil
	}
	cfg, err := helixon.LoadConfig(path)
	if err != nil {
		return helixon.RuntimeConfig{}, err
	}
	cfg.Logger = slog.Default()
	cfg.Memory = resolveMemoryURL(cfg.Memory, os.Getenv("HELIXON_ENGRAM_URL"))
	return cfg, nil
}

// newMemoryCmd wires `helixon memory backend` (v17802 MVP-5 Engram wire).
//
// Default backend selection follows the v17802 design: when
// $HELIXON_ENGRAM_URL is set (and reachable) the runtime uses
// EngramBackend with an InMemoryBackend fail-open; otherwise it
// uses InMemoryBackend directly. The choice is logged so operators
// can verify which path is active at boot.
func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Memory backend diagnostics (v17802 MVP-5)",
	}
	cmd.AddCommand(newMemoryBackendCmd())
	return cmd
}

func newMemoryBackendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backend",
		Short: "Print the active memory backend type and stats",
		RunE: func(cmd *cobra.Command, _ []string) error {
			b := defaultMemoryBackend()
			st := b.Stats()
			fmt.Fprintf(cmd.OutOrStdout(), "backend: %s\n", st.Backend)
			fmt.Fprintf(cmd.OutOrStdout(), "count:   %d\n", st.Count)
			fmt.Fprintf(cmd.OutOrStdout(), "stores:  %d\n", st.StoreCount)
			fmt.Fprintf(cmd.OutOrStdout(), "recalls: %d\n", st.RecallCount)
			fmt.Fprintf(cmd.OutOrStdout(), "searches:%d\n", st.SearchCount)
			fmt.Fprintf(cmd.OutOrStdout(), "fallback:%d\n", st.FallbackCount)
			_ = b.Close()
			return nil
		},
	}
	return cmd
}

// defaultMemoryBackend is the v17802 wire-up point. The
// InMemoryBackend is always the local fail-open; the EngramBackend
// is added when HELIXON_ENGRAM_URL is set. Tests override via
// SetDefaultMemoryBackend.
func defaultMemoryBackend() memory.Backend {
	fb := memory.NewInMemoryBackend()
	if url := os.Getenv("HELIXON_ENGRAM_URL"); url != "" {
		return memory.NewEngramBackend(memory.EngramConfig{BaseURL: url}, fb)
	}
	return fb
}
