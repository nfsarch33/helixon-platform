package helixon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
)

// Phase represents the current lifecycle stage of the runtime.
type Phase string

const (
	PhaseCreated    Phase = "created"
	PhaseInit       Phase = "init"
	PhaseConfigured Phase = "configured"
	PhaseRunning    Phase = "running"
	PhaseShutdown   Phase = "shutdown"
)

// RuntimeConfig holds all configuration for a Helixon agent runtime.
type RuntimeConfig struct {
	AgentID                 string
	TenantID                string
	SystemPrompt            string
	SessionDSN              string
	MaxIterations           int
	MaxTokens               int
	Timeout                 time.Duration
	HeartbeatEvery          time.Duration
	Provider                ProviderConfig
	SprintboardURL          string
	SprintboardCapabilities string
	Logger                  *slog.Logger
	// Sandbox describes the execution boundary for tool calls.
	Sandbox sandbox.Config
	// Completion gates run completion behind verifier evidence.
	Completion agent.CompletionPolicy
	// LoopGuard bounds identical repeated tool calls.
	LoopGuard LoopGuardConfig
	// Agentrace records one NDJSON event per tool call.
	Agentrace AgentraceConfig
	// Tickets configures the serve-mode ticket poller. Default OFF.
	Tickets TicketPollerConfig
	// Metrics configures the Prometheus agent-runtime series. Default ON.
	Metrics MetricsConfig
	// Memory configures loop memory (Engram) on the ticket path.
	Memory LoopMemoryConfig
}

// MetricsConfig configures the agent runtime metrics.
type MetricsConfig struct {
	Enabled bool
}

// LoopMemoryConfig configures loop memory on the ticket path: what the agent
// recalls before a run and what it writes back after one.
type LoopMemoryConfig struct {
	Enabled     bool
	EngramURL   string
	AppID       string
	WorkspaceID string
	MaxContext  int
	// Timeout bounds a single memory operation. It exists because a memory
	// backend that is DOWN must degrade the agent, not stall it: the Engram
	// client retries, and without a ceiling a dead server could eat a
	// meaningful slice of the per-ticket budget before the first model call.
	Timeout time.Duration
}

func (c LoopMemoryConfig) withDefaults() LoopMemoryConfig {
	if c.AppID == "" {
		c.AppID = "helixon"
	}
	if c.MaxContext <= 0 {
		c.MaxContext = 5
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultMemoryOpTimeout
	}
	return c
}

// Active reports whether loop memory should actually be wired. The flag alone
// is not enough: `enabled` defaults true, and without a configured server there
// is nothing to talk to, so an upgraded binary must not start reaching for one.
func (c LoopMemoryConfig) Active() bool {
	return c.Enabled && strings.TrimSpace(c.EngramURL) != ""
}

// DefaultMemoryOpTimeout bounds one Engram call.
const DefaultMemoryOpTimeout = 15 * time.Second

// LoopGuardConfig configures the loop detector wired in front of the tool
// executor.
type LoopGuardConfig struct {
	Enabled   bool
	Threshold int
	Window    time.Duration
}

// AgentraceConfig configures the NDJSON tool-call trace.
type AgentraceConfig struct {
	Enabled bool
	LogPath string
}

func (c RuntimeConfig) withDefaults() RuntimeConfig {
	if c.AgentID == "" {
		c.AgentID = "helixon-default"
	}
	if c.SessionDSN == "" {
		c.SessionDSN = "file:helixon-sessions.db?cache=shared&mode=rwc"
	}
	if c.MaxIterations <= 0 {
		c.MaxIterations = 25
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 128000
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Minute
	}
	if c.HeartbeatEvery <= 0 {
		c.HeartbeatEvery = 60 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	// Applied here as well as in the YAML decoder so a RuntimeConfig built in
	// code — which every test does — cannot end up with a zero memory timeout.
	// context.WithTimeout(ctx, 0) is already expired, so the failure would be
	// "memory never works, silently", which is the hardest kind to notice.
	c.Memory = c.Memory.withDefaults()
	return c
}

// Runtime is the top-level lifecycle coordinator for a Helixon agent.
// It composes the agent loop, channels, tool registry, memory, and
// control plane into a single managed lifecycle.
type Runtime struct {
	mu     sync.RWMutex
	phase  Phase
	cfg    RuntimeConfig
	logger *slog.Logger

	provider   llm.Provider
	registry   *tooldispatch.Registry
	executor   agent.ToolExecutor
	traced     *tooldispatch.TracedExecutor
	sandboxRun *sandbox.Runner
	store      *agent.SessionStore
	agent      *agent.Agent
	memory     *memory.HybridSearcher
	agentMem   *memory.AgentMemory
	metrics    *agentmetrics.Metrics
	sprintCtl  *controlplane.SprintboardClient
	poller     *TicketPoller
	channels   []Channel
	cancelFunc context.CancelFunc
}

// NewRuntime creates a runtime in the Created phase. Call Init() to
// bootstrap stores and registries, then Configure() to wire channels
// and tools, then Run() to start serving.
func NewRuntime(provider llm.Provider, cfg RuntimeConfig) *Runtime {
	cfg = cfg.withDefaults()
	return &Runtime{
		phase:    PhaseCreated,
		cfg:      cfg,
		logger:   cfg.Logger.With(slog.String("component", "helixon.runtime")),
		provider: provider,
	}
}

// Phase returns the current lifecycle phase.
func (r *Runtime) Phase() Phase {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.phase
}

// Init bootstraps the session store, tool registry, and memory subsystem.
// Transitions: Created -> Init.
func (r *Runtime) Init(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.phase != PhaseCreated {
		return fmt.Errorf("helixon: Init requires phase Created, got %s", r.phase)
	}

	store, err := agent.NewSessionStore(ctx, r.cfg.SessionDSN)
	if err != nil {
		return fmt.Errorf("helixon: init session store: %w", err)
	}
	r.store = store

	r.registry = tooldispatch.NewRegistry(r.logger)
	r.executor = r.registry

	r.phase = PhaseInit
	r.logger.Info("runtime initialised",
		slog.String("agent_id", r.cfg.AgentID),
		slog.String("dsn", r.cfg.SessionDSN),
	)
	return nil
}

// Configure wires channels, memory, and control plane connections.
// Transitions: Init -> Configured.
func (r *Runtime) Configure(ctx context.Context, opts ...ConfigOption) error { //nolint:revive // unused-parameter required by interface
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.phase != PhaseInit {
		return fmt.Errorf("helixon: Configure requires phase Init, got %s", r.phase)
	}

	for _, opt := range opts {
		if err := opt(r); err != nil {
			return fmt.Errorf("helixon: config option: %w", err)
		}
	}

	agentCfg := agent.Config{
		MaxIterations: r.cfg.MaxIterations,
		MaxTokens:     r.cfg.MaxTokens,
		Timeout:       r.cfg.Timeout,
		SystemPrompt:  r.cfg.SystemPrompt,
		Logger:        r.logger,
		Completion:    r.cfg.Completion,
	}
	// Assigned through an explicit nil check: a typed nil placed straight into
	// the interface would make every `Observer != nil` guard in the loop pass
	// while holding nothing.
	if r.metrics != nil {
		agentCfg.Observer = r.metrics
	}
	r.agent = agent.New(r.provider, r.executor, r.store, agentCfg)

	if err := r.buildTicketPoller(); err != nil {
		return err
	}

	r.phase = PhaseConfigured
	r.logger.Info("runtime configured",
		slog.Int("channels", len(r.channels)),
		slog.Int("tools", len(r.registry.Names())),
	)
	return nil
}

// Run starts all channels, the heartbeat loop, and blocks until the
// context is cancelled or Shutdown is called. Transitions: Configured -> Running.
func (r *Runtime) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.phase != PhaseConfigured {
		r.mu.Unlock()
		return fmt.Errorf("helixon: Run requires phase Configured, got %s", r.phase)
	}
	r.phase = PhaseRunning
	ctx, cancel := context.WithCancel(ctx)
	r.cancelFunc = cancel
	r.mu.Unlock()

	r.logger.Info("runtime starting", slog.Int("channels", len(r.channels)))

	if r.sprintCtl != nil {
		if err := r.sprintCtl.Register(ctx); err != nil {
			r.logger.Warn("sprintboard registration failed (non-fatal)", slog.String("error", err.Error()))
		}
	}

	var wg sync.WaitGroup

	// Runs whose worker died (this process, last time) are finished first, so
	// a ticket left claimed by a crash is completed or escalated instead of
	// sitting claimed until a human notices.
	wg.Add(1)
	go func() {
		defer wg.Done()
		stats, err := r.RecoverInterruptedRuns(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			r.logger.Warn("run recovery stopped early", slog.String("error", err.Error()))
		}
		r.logger.Info("run recovery finished",
			slog.Int("dead_lettered", stats.DeadLettered), slog.Int("resumed", stats.Resumed),
			slog.Int("completed", stats.Completed), slog.Int("escalated", stats.Escalated), slog.Int("failed", stats.Failed))
	}()

	if r.sprintCtl != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.heartbeatLoop(ctx)
		}()
	}

	if r.poller != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.poller.Run(ctx); err != nil {
				r.logger.Error("ticket poller stopped", slog.String("error", err.Error()))
			}
			stats := r.poller.Stats()
			r.logger.Info("ticket poller finished",
				slog.Int("polls", stats.Polls),
				slog.Int("claimed", stats.Claimed),
				slog.Int("conflicts", stats.Conflicts),
				slog.Int("completed", stats.Completed),
				slog.Int("escalated", stats.Escalated),
				slog.Int("abandoned", stats.Abandoned),
				slog.Int("errors", stats.Errors),
			)
		}()
	}

	handler := r.HandleMessage

	errCh := make(chan error, len(r.channels))
	for _, ch := range r.channels {
		wg.Add(1)
		go func(c Channel) {
			defer wg.Done()
			if err := c.Serve(ctx, handler); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("channel %s: %w", c.Name(), err)
			}
		}(ch)
	}

	select {
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	case <-ctx.Done():
		wg.Wait()
		return nil
	}
}

// Shutdown gracefully stops the runtime. Transitions: Running -> Shutdown.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.phase != PhaseRunning {
		r.mu.Unlock()
		return fmt.Errorf("helixon: Shutdown requires phase Running, got %s", r.phase)
	}
	r.phase = PhaseShutdown
	if r.cancelFunc != nil {
		r.cancelFunc()
	}
	r.mu.Unlock()

	r.logger.Info("runtime shutting down")

	var firstErr error
	for _, ch := range r.channels {
		if err := ch.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("shutdown channel %s: %w", ch.Name(), err)
		}
	}

	if r.traced != nil {
		if err := r.traced.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close agentrace sink: %w", err)
		}
	}

	if r.store != nil {
		if err := r.store.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close session store: %w", err)
		}
	}

	r.logger.Info("runtime stopped")
	return firstErr
}

// Registry returns the tool registry for external tool registration.
func (r *Runtime) Registry() *tooldispatch.Registry {
	return r.registry
}

// Executor returns the fully decorated tool executor the agent loop uses:
// the registry wrapped by whichever of the sandbox gate, loop guard, and
// agentrace recorder were configured. Exposed so the decorator chain can be
// exercised directly, without driving a whole model conversation to find out
// whether a guardrail is actually in the path.
func (r *Runtime) Executor() agent.ToolExecutor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.executor
}

// HandleMessage processes an incoming message through the agent loop.
// It creates a session if none is specified and runs the full tool-augmented
// conversation loop.
func (r *Runtime) HandleMessage(ctx context.Context, msg IncomingMessage) (string, error) {
	sessionID := msg.SessionID
	if sessionID == "" {
		sess, err := r.store.CreateSession(ctx, r.cfg.AgentID, map[string]string{
			"channel": msg.Channel,
		})
		if err != nil {
			return "", fmt.Errorf("create session: %w", err)
		}
		sessionID = sess.ID
	}

	result, err := r.agent.Run(ctx, sessionID, msg.Content)
	if err != nil {
		return "", err
	}
	return result.FinalContent, nil
}

// RecoveryStats summarizes one RecoverInterruptedRuns sweep.
type RecoveryStats struct {
	DeadLettered int
	Resumed      int
	Completed    int
	Escalated    int
	Failed       int
}

// RecoverInterruptedRuns finishes every run whose lease has lapsed: runs that
// spent their attempt budget are dead-lettered, the rest are resumed from the
// durable log by the same agent loop, and a ticket run's outcome is reported
// to the board through the poller's one report path (complete with evidence,
// or escalate). It runs at start-up and may be called by an operator command.
// Runs are handled sequentially: recovery competes with live work for the
// same model and tool budgets, and a burst of parallel resumes after a fleet
// restart is the one thing this must not become.
func (r *Runtime) RecoverInterruptedRuns(ctx context.Context) (RecoveryStats, error) {
	var st RecoveryStats
	if r.store == nil || r.agent == nil {
		return st, errors.New("helixon: recovery needs an initialized runtime")
	}
	dead, err := r.store.DeadLetterExhausted(ctx, r.agent.MaxRunAttempts())
	if err != nil {
		return st, fmt.Errorf("dead-letter exhausted runs: %w", err)
	}
	st.DeadLettered = dead
	runs, err := r.store.ListInterruptedRuns(ctx)
	if err != nil {
		return st, fmt.Errorf("list interrupted runs: %w", err)
	}
	for i := range runs {
		run := &runs[i]
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		res, runErr := r.agent.Resume(ctx, run.ID)
		if errors.Is(runErr, agent.ErrLeaseHeld) || errors.Is(runErr, agent.ErrRunFinished) {
			continue // another worker took it, or finished it, between the listing and the claim
		}
		st.Resumed++
		final := ""
		if res != nil {
			final = res.FinalContent
		}
		if runErr == nil {
			st.Completed++
		} else {
			st.Failed++
		}
		if tid := run.Meta["ticket_id"]; tid != "" && r.poller != nil {
			if r.poller.ReportRecovered(ctx, tid, final, runErr) {
				st.Escalated++
			}
		}
		r.logger.Info("recovered interrupted run", slog.String("run_id", run.ID),
			slog.String("ticket_id", run.Meta["ticket_id"]), slog.Int("attempts", run.Attempts),
			slog.Bool("completed", runErr == nil))
	}
	return st, nil
}

// Store exposes the session/run store for read-only consumers such as the
// operator console. Nil before Init.
func (r *Runtime) Store() *agent.SessionStore { return r.store }

// Memory exposes the hybrid memory searcher when one was configured; nil
// otherwise, which the console reports as "memory not configured".
func (r *Runtime) Memory() *memory.HybridSearcher { return r.memory }

// buildTicketPoller constructs the serve-mode poller when the feature flag is
// on. It runs during Configure, not Run, so a misconfiguration (no board, a
// per-ticket budget shorter than the agent's own timeout) fails at start-up
// with a named reason instead of at 3am with a board full of stuck claims.
func (r *Runtime) buildTicketPoller() error {
	if !r.cfg.Tickets.Enabled {
		return nil
	}
	if r.sprintCtl == nil {
		return errors.New("helixon: tickets.enabled is set but no sprintboard client is wired; " +
			"pass WithSprintboard (set sprintboard.url in the config)")
	}
	p, err := NewTicketPoller(r.cfg.Tickets, r.sprintCtl, r.runTicketWork, r.cfg.AgentID, r.cfg.Timeout, r.logger,
		WithPollerMetrics(r.metrics))
	if err != nil {
		return fmt.Errorf("helixon: ticket poller: %w", err)
	}
	r.poller = p
	return nil
}

// Metrics returns the agent-runtime metrics, or nil when they are off.
// Exposed so a caller can assert that the wiring actually happened rather than
// inferring it from a config flag that nothing read.
func (r *Runtime) Metrics() *agentmetrics.Metrics {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metrics
}

// AgentMemory returns the wired loop memory, or nil. Same reason as Metrics.
func (r *Runtime) AgentMemory() *memory.AgentMemory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agentMem
}

// TicketPoller returns the configured poller, or nil when ticket polling is
// off. Exposed so a test can assert the flag actually decided something.
func (r *Runtime) TicketPoller() *TicketPoller {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.poller
}

// runTicketWork is the TicketWorker the poller drives: one fresh session per
// ticket, then the same agent loop (and the same verifier gate) that every
// other entry point uses.
//
// It returns the partial final content ALONGSIDE the error, because the
// escalation comment is only useful if it carries what the agent actually
// said before it gave up.
//
//nolint:gocritic // hugeParam: the TicketWorker contract takes the ticket by value
func (r *Runtime) runTicketWork(ctx context.Context, ticket controlplane.Ticket) (string, error) {
	sess, err := r.store.CreateSession(ctx, r.cfg.AgentID, map[string]string{
		"channel":   "ticket",
		"ticket_id": ticket.ID,
	})
	if err != nil {
		return "", fmt.Errorf("create ticket session: %w", err)
	}
	prompt := TicketPrompt(ticket)
	prompt = r.withRecalledContext(ctx, prompt)
	// A durable run carrying the ticket id, so a worker that dies mid-ticket
	// leaves something RecoverInterruptedRuns can finish and report.
	result, runErr := r.agent.RunDurable(ctx, agent.NewRunID(), sess.ID, prompt, map[string]string{
		"channel": "ticket", "ticket_id": ticket.ID,
	})
	final := ""
	if result != nil {
		final = result.FinalContent
	}
	r.persistTicketMemory(ctx, ticket, final, runErr)
	return final, runErr
}

// withRecalledContext prepends whatever loop memory knows about this ticket.
//
// Every failure mode here returns the prompt unchanged. A memory backend being
// down degrades the agent — it works without recall, as it did before this
// existed — and must never be able to fail a ticket, so there is deliberately
// no error path out of this function for a caller to mishandle.
func (r *Runtime) withRecalledContext(ctx context.Context, prompt string) string {
	am := r.AgentMemory()
	if am == nil {
		return prompt
	}
	memCtx, cancel := context.WithTimeout(ctx, r.cfg.Memory.Timeout)
	defer cancel()
	recalled := strings.TrimSpace(am.RetrieveContext(memCtx, prompt))
	if recalled == "" {
		return prompt
	}
	r.logger.Debug("loop memory recalled context", slog.Int("bytes", len(recalled)))
	return recalled + "\n\n" + prompt
}

// persistTicketMemory writes a summary of the run back to memory.
//
// The summary is built the same way `helixon task`'s Engram persistence builds
// its own (agent, prompt, result), with the OUTCOME included: a failed run is
// the most useful thing this agent can remember, and a memory store that only
// records successes teaches it that everything works.
//
// The context is detached and re-bounded because this runs after the agent
// loop, which may have exhausted the per-ticket deadline. Reusing the expired
// context would mean the runs most worth remembering are exactly the ones never
// written down.
//
//nolint:gocritic // hugeParam: the ticket is a value snapshot, see runTicketWork
func (r *Runtime) persistTicketMemory(ctx context.Context, ticket controlplane.Ticket, result string, runErr error) {
	am := r.AgentMemory()
	if am == nil {
		return
	}
	summary := TicketMemorySummary(r.cfg.AgentID, ticket, result, runErr)
	if summary == "" {
		return
	}
	memCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.Memory.Timeout)
	defer cancel()
	if err := am.StoreConversationSummary(memCtx, summary); err != nil {
		r.logger.Warn("loop memory write failed (non-fatal; the ticket verdict is unaffected)",
			slog.String("ticket", ticket.ID), slog.String("error", err.Error()))
	}
}

func (r *Runtime) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.HeartbeatEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.sprintCtl.Register(ctx); err != nil {
				r.logger.Warn("heartbeat failed", slog.String("error", err.Error()))
			}
		}
	}
}

// ConfigOption applies a configuration to the runtime during the Configure phase.
type ConfigOption func(*Runtime) error

// WithChannel adds a serving channel to the runtime.
func WithChannel(ch Channel) ConfigOption {
	return func(r *Runtime) error {
		r.channels = append(r.channels, ch)
		return nil
	}
}

// WithMemory wires the hybrid memory searcher.
func WithMemory(m *memory.HybridSearcher) ConfigOption {
	return func(r *Runtime) error {
		r.memory = m
		return nil
	}
}

// WithSprintboard wires the sprintboard control plane client.
func WithSprintboard(client *controlplane.SprintboardClient) ConfigOption {
	return func(r *Runtime) error {
		r.sprintCtl = client
		return nil
	}
}

// WithAgentMemory wires loop memory onto the ticket path: recall before a run,
// a summary after one.
//
// Every piece of this has existed and been tested since v17802 and was wired
// into nothing — AgentMemory.RetrieveContext and StoreConversationSummary were
// referenced only by their own tests. This is the wiring, not new machinery.
func WithAgentMemory(am *memory.AgentMemory) ConfigOption {
	return func(r *Runtime) error {
		r.agentMem = am
		return nil
	}
}

// WithAgentMetrics installs the runtime metrics and puts the metered decorator
// at the OUTSIDE of the executor chain.
//
// Outermost is the whole point. The sandbox gate and the loop guard both refuse
// calls without ever reaching the registry, so a counter installed under them
// would report a quiet, healthy agent for one whose every tool call was being
// denied.
//
// Apply it LAST in the option list; it wraps whatever the earlier options
// built.
func WithAgentMetrics(m *agentmetrics.Metrics) ConfigOption {
	return func(r *Runtime) error {
		if m == nil {
			return nil
		}
		if r.executor == nil {
			return errors.New("helixon: WithAgentMetrics requires Init to have run")
		}
		r.metrics = m
		r.executor = agentmetrics.NewMeteredExecutor(r.executor, m)
		return nil
	}
}

// WithAgentrace wraps the current tool executor in a TracedExecutor that
// appends NDJSON events to the configured log path. The runtime takes
// ownership of the underlying sink and closes it during Shutdown.
func WithAgentrace(cfg tooldispatch.AgentraceConfig) ConfigOption {
	return func(r *Runtime) error {
		if r.executor == nil {
			return fmt.Errorf("helixon: WithAgentrace requires Init to have run")
		}
		if cfg.AgentID == "" {
			cfg.AgentID = r.cfg.AgentID
		}
		traced, err := tooldispatch.NewTracedExecutor(r.executor, cfg, r.logger)
		if err != nil {
			return fmt.Errorf("helixon: agentrace: %w", err)
		}
		r.executor = traced
		r.traced = traced
		return nil
	}
}

// WithSandbox wraps the current tool executor in the sandbox gate, so one
// decorator covers shell, file_write, and every tool registered after it.
// Modeled on WithAgentrace and applied during Configure, before agent.New
// consumes the executor.
//
// Three outcomes, all of them loud:
//
//   - sandbox disabled and host execution NOT explicitly permitted: an error.
//     A config that switches the boundary off without saying so is the case
//     this whole change exists to remove.
//   - sandbox disabled and allow_unsandboxed_host_execution set: no gate is
//     installed, and a warning names the risk on every start.
//   - sandbox enabled: the gate is installed.
//
//nolint:gocritic // hugeParam: sandbox.Config is passed by value by design
func WithSandbox(cfg sandbox.Config) ConfigOption {
	return func(r *Runtime) error {
		runner, err := sandbox.NewRunner(cfg)
		if err != nil {
			return fmt.Errorf("helixon: sandbox: %w", err)
		}
		return applySandbox(r, runner, cfg.AllowUnsandboxedHostExecution)
	}
}

// WithSandboxRunner is WithSandbox for callers that already built the runner,
// which the CLI does because the verifier tool must be registered against the
// same runner before Configure runs.
func WithSandboxRunner(runner *sandbox.Runner, allowHostExecution bool) ConfigOption {
	return func(r *Runtime) error {
		return applySandbox(r, runner, allowHostExecution)
	}
}

func applySandbox(r *Runtime, runner *sandbox.Runner, allowHostExecution bool) error {
	if r.executor == nil {
		return errors.New("helixon: WithSandbox requires Init to have run")
	}
	if runner == nil {
		if !allowHostExecution {
			return errors.New("helixon: sandbox is disabled but sandbox.allow_unsandboxed_host_execution is not set; " +
				"either enable the sandbox or state explicitly that agent tools may execute on this host with no boundary")
		}
		r.logger.Warn("SANDBOX DISABLED: agent tool commands will execute directly on this host with no container boundary, no network isolation, and this process's full ambient authority (sandbox.allow_unsandboxed_host_execution is set)")
		return nil
	}
	cfg := runner.Config()
	policy := sandbox.PolicyFor(cfg)
	exec, err := sandbox.NewExecutor(r.executor, runner, policy, r.logger)
	if err != nil {
		return fmt.Errorf("helixon: sandbox executor: %w", err)
	}
	r.executor = exec
	r.sandboxRun = runner
	r.logger.Info("sandbox gate installed",
		slog.String("engine", cfg.Engine),
		slog.String("image", cfg.Image),
		slog.String("network", cfg.Network),
		slog.String("workspace", cfg.Workspace),
		slog.String("workspace_access", string(cfg.WorkspaceAccess)),
		slog.String("unlisted_tools", policy.Default.String()),
	)
	return nil
}

// SandboxRunner returns the installed sandbox runner, or nil.
func (r *Runtime) SandboxRunner() *sandbox.Runner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sandboxRun
}

// WithLoopGuard wraps the current tool executor in the existing
// tooldispatch.LoopGuardExecutor. The machinery has been in the tree since
// v17003 and was wired into nothing; a repeated identical tool call now
// fails fast instead of burning the token budget.
func WithLoopGuard(cfg LoopGuardConfig) ConfigOption {
	return func(r *Runtime) error {
		if !cfg.Enabled {
			return nil
		}
		if r.executor == nil {
			return errors.New("helixon: WithLoopGuard requires Init to have run")
		}
		guard := loopguard.New(cfg.Threshold, cfg.Window)
		logger := r.logger
		r.executor = tooldispatch.NewLoopGuardExecutor(r.executor, guard).
			WithOnDetect(func(toolName, hash string) {
				logger.Warn("loop guard tripped",
					slog.String("tool", toolName),
					slog.String("hash", hash),
				)
			})
		return nil
	}
}
