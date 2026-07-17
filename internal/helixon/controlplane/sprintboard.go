package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// SprintboardConfig configures the sprintboard auto-registration.
type SprintboardConfig struct {
	BaseURL      string
	AgentName    string
	Capabilities string
	TenantID     string // optional: stamped on every outbound payload (v18685-1)
	Logger       *slog.Logger
}

func (c SprintboardConfig) withDefaults() SprintboardConfig {
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:8585"
	}
	if c.AgentName == "" {
		c.AgentName = "helixon-agent"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// SprintboardClient handles sprintboard registration and ticket operations.
type SprintboardClient struct {
	cfg    SprintboardConfig
	http   *http.Client
	logger *slog.Logger
}

// NewSprintboardClient creates a client for the sprintboard API.
func NewSprintboardClient(cfg SprintboardConfig, logger *slog.Logger) *SprintboardClient {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &SprintboardClient{
		cfg:    cfg,
		http:   &http.Client{Timeout: 10 * time.Second},
		logger: logger.With(slog.String("component", "helixon.controlplane.sprintboard")),
	}
}

// AgentRegistration is the payload for auto-registration.
type AgentRegistration struct {
	AgentID      string `json:"agent_id"`
	TenantID     string `json:"tenant_id,omitempty"`
	Capabilities string `json:"capabilities"`
	Status       string `json:"status"`
	RegisteredAt string `json:"registered_at"`
}

// Register auto-registers this agent with the sprintboard on startup.
func (c *SprintboardClient) Register(ctx context.Context) error {
	reg := AgentRegistration{
		AgentID:      c.cfg.AgentName,
		TenantID:     c.cfg.TenantID,
		Capabilities: c.cfg.Capabilities,
		Status:       "active",
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, _ := json.Marshal(reg)
	resp, err := c.doPost(ctx, "/api/v1/agents", data)
	if err != nil {
		return fmt.Errorf("sprintboard register: %w", err)
	}
	_ = resp

	c.logger.Info("registered with sprintboard",
		slog.String("agent", c.cfg.AgentName),
		slog.String("url", c.cfg.BaseURL),
	)
	return nil
}

// Ticket represents a sprintboard ticket.
type Ticket struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Assignee string `json:"assignee,omitempty"`
	Priority int    `json:"priority"`
}

// ClaimTicket atomically claims a ticket for this agent.
func (c *SprintboardClient) ClaimTicket(ctx context.Context, ticketID string) error {
	data, _ := json.Marshal(map[string]string{
		"agent_id":  c.cfg.AgentName,
		"tenant_id": c.cfg.TenantID,
	})
	path := fmt.Sprintf("/api/v1/tickets/%s/claim", ticketID)
	_, err := c.doPost(ctx, path, data)
	return err
}

// CompleteTicket marks a ticket as completed with evidence.
func (c *SprintboardClient) CompleteTicket(ctx context.Context, ticketID, evidence string) error {
	data, _ := json.Marshal(map[string]string{
		"agent_id": c.cfg.AgentName,
		"evidence": evidence,
	})
	path := fmt.Sprintf("/api/v1/tickets/%s/complete", ticketID)
	_, err := c.doPost(ctx, path, data)
	return err
}

// SprintStatus returns the current sprint status.
func (c *SprintboardClient) SprintStatus(ctx context.Context, sprintID string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/sprints/%s", c.cfg.BaseURL, sprintID), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	_ = resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sprintboard error %d: %s", resp.StatusCode, string(data))
	}

	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("decode sprint status: %w", err)
	}
	return status, nil
}

//nolint:unparam // body is required by the contract; one caller reads the response, the others discard.
func (c *SprintboardClient) doPost(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	_ = resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sprintboard error %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}
