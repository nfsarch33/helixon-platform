package helixon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
	"github.com/nfsarch33/helixon-platform/internal/llm"
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
	store      *agent.SessionStore
	agent      *agent.Agent
	memory     *memory.HybridSearcher
	sprintCtl  *controlplane.SprintboardClient
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

	r.agent = agent.New(r.provider, r.executor, r.store, agent.Config{
		MaxIterations: r.cfg.MaxIterations,
		MaxTokens:     r.cfg.MaxTokens,
		Timeout:       r.cfg.Timeout,
		SystemPrompt:  r.cfg.SystemPrompt,
		Logger:        r.logger,
	})

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

	if r.sprintCtl != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.heartbeatLoop(ctx)
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
