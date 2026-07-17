package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// HeartbeatConfig configures the periodic health reporting system.
type HeartbeatConfig struct {
	Interval time.Duration
	AgentID  string
	TenantID string // optional: stamped on every HeartbeatPayload (v18685-1)
	Logger   *slog.Logger
}

func (c HeartbeatConfig) withDefaults() HeartbeatConfig {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.AgentID == "" {
		c.AgentID = "helixon-agent"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// HeartbeatPayload is the periodic health report.
type HeartbeatPayload struct {
	AgentID      string            `json:"agent_id"`
	TenantID     string            `json:"tenant_id,omitempty"`
	Status       string            `json:"status"`
	Uptime       string            `json:"uptime"`
	Iterations   int               `json:"iterations"`
	TokensUsed   int               `json:"tokens_used"`
	ActiveSessID string            `json:"active_session_id,omitempty"`
	Extras       map[string]string `json:"extras,omitempty"`
	Timestamp    time.Time         `json:"timestamp"`
}

// HeartbeatSink receives heartbeat payloads.
type HeartbeatSink interface {
	SendHeartbeat(ctx context.Context, payload HeartbeatPayload) error
}

// A2AHeartbeatSink sends heartbeats to the A2A gateway.
type A2AHeartbeatSink struct {
	client *A2AClient
}

// NewA2AHeartbeatSink creates a sink that sends heartbeats via the A2A client.
func NewA2AHeartbeatSink(client *A2AClient) *A2AHeartbeatSink {
	return &A2AHeartbeatSink{client: client}
}

// SendHeartbeat sends a heartbeat to the A2A gateway.
func (s *A2AHeartbeatSink) SendHeartbeat(ctx context.Context, payload HeartbeatPayload) error {
	data, _ := json.Marshal(payload)
	_, err := s.client.doPost(ctx, "/api/v1/agents/heartbeat", data)
	return err
}

// LogHeartbeatSink logs heartbeats via slog for debugging.
type LogHeartbeatSink struct {
	Logger *slog.Logger
}

// SendHeartbeat logs the heartbeat payload.
func (s *LogHeartbeatSink) SendHeartbeat(_ context.Context, payload HeartbeatPayload) error {
	s.Logger.Info("heartbeat",
		slog.String("agent_id", payload.AgentID),
		slog.String("status", payload.Status),
		slog.String("uptime", payload.Uptime),
		slog.Int("iterations", payload.Iterations),
		slog.Int("tokens_used", payload.TokensUsed),
	)
	return nil
}

// HeartbeatMonitor runs periodic heartbeats on a background goroutine.
type HeartbeatMonitor struct {
	cfg       HeartbeatConfig
	sink      HeartbeatSink
	startedAt time.Time
	logger    *slog.Logger

	mu           sync.Mutex
	iterations   int
	tokensUsed   int
	activeSessID string
	extras       map[string]string
}

// NewHeartbeatMonitor creates a monitor that sends periodic heartbeats.
func NewHeartbeatMonitor(sink HeartbeatSink, cfg HeartbeatConfig) *HeartbeatMonitor {
	cfg = cfg.withDefaults()
	return &HeartbeatMonitor{
		cfg:       cfg,
		sink:      sink,
		startedAt: time.Now(),
		logger:    cfg.Logger.With(slog.String("component", "helixon.controlplane.heartbeat")),
		extras:    make(map[string]string),
	}
}

// Start begins sending heartbeats. Returns a cancel function to stop.
func (m *HeartbeatMonitor) Start(ctx context.Context) context.CancelFunc {
	hbCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(m.cfg.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				payload := m.buildPayload()
				if err := m.sink.SendHeartbeat(hbCtx, payload); err != nil {
					m.logger.Warn("heartbeat failed", slog.String("error", err.Error()))
				}
			}
		}
	}()
	return cancel
}

// Update records the current agent state for the next heartbeat.
func (m *HeartbeatMonitor) Update(iterations, tokensUsed int, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.iterations = iterations
	m.tokensUsed = tokensUsed
	m.activeSessID = sessionID
}

// SetExtra adds a custom key-value pair to the heartbeat extras.
func (m *HeartbeatMonitor) SetExtra(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extras[key] = value
}

func (m *HeartbeatMonitor) buildPayload() HeartbeatPayload {
	m.mu.Lock()
	defer m.mu.Unlock()

	extrasCopy := make(map[string]string, len(m.extras))
	for k, v := range m.extras {
		extrasCopy[k] = v
	}

	return HeartbeatPayload{
		AgentID:      m.cfg.AgentID,
		TenantID:     m.cfg.TenantID,
		Status:       "running",
		Uptime:       time.Since(m.startedAt).Round(time.Second).String(),
		Iterations:   m.iterations,
		TokensUsed:   m.tokensUsed,
		ActiveSessID: m.activeSessID,
		Extras:       extrasCopy,
		Timestamp:    time.Now().UTC(),
	}
}

// Uptime returns the duration since the monitor started.
func (m *HeartbeatMonitor) Uptime() time.Duration {
	return time.Since(m.startedAt)
}

// SendNow sends a heartbeat immediately outside the regular interval.
func (m *HeartbeatMonitor) SendNow(ctx context.Context) error {
	return m.sink.SendHeartbeat(ctx, m.buildPayload())
}

// SendShutdown sends a final heartbeat with status "shutting_down".
func (m *HeartbeatMonitor) SendShutdown(ctx context.Context) error {
	payload := m.buildPayload()
	payload.Status = "shutting_down"
	return m.sink.SendHeartbeat(ctx, payload)
}

// FleetDailyReport generates a daily summary suitable for fleet reporting.
func FleetDailyReport(agentID string, sessions int, tokensIn, tokensOut int, costUSD float64, uptime time.Duration) string {
	return fmt.Sprintf(
		"=== Fleet Daily Report ===\nAgent: %s\nSessions: %d\nTokens In: %d | Out: %d\nCost: $%.4f\nUptime: %s\nGenerated: %s\n",
		agentID, sessions, tokensIn, tokensOut, costUSD,
		uptime.Round(time.Second),
		time.Now().UTC().Format(time.RFC3339),
	)
}
