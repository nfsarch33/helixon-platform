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

// A2AConfig configures the Agent-to-Agent gateway client.
type A2AConfig struct {
	GatewayURL string
	AgentName  string
	AgentID    string
	BearerAuth string
	Timeout    time.Duration
}

func (c A2AConfig) withDefaults() A2AConfig {
	if c.GatewayURL == "" {
		c.GatewayURL = "http://localhost:9090"
	}
	if c.AgentName == "" {
		c.AgentName = "helixon-agent"
	}
	if c.Timeout <= 0 {
		c.Timeout = 15 * time.Second
	}
	return c
}

// AgentCard is the registration payload for the A2A gateway.
type AgentCard struct {
	Name         string   `json:"name"`
	ID           string   `json:"id"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Endpoint     string   `json:"endpoint,omitempty"`
	Status       string   `json:"status"`
	RegisteredAt string   `json:"registered_at,omitempty"`
}

// TaskAssignment represents a task received from the A2A gateway.
type TaskAssignment struct {
	TaskID    string         `json:"task_id"`
	AgentName string         `json:"agent_name"`
	Input     map[string]any `json:"input"`
	Priority  int            `json:"priority"`
}

// TaskResult is the completion payload sent back to the gateway.
type TaskResult struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Output  any    `json:"output"`
	Error   string `json:"error,omitempty"`
	Elapsed string `json:"elapsed,omitempty"`
}

// A2AClient communicates with the A2A gateway for agent registration and task coordination.
type A2AClient struct {
	cfg    A2AConfig
	http   *http.Client
	logger *slog.Logger
}

// NewA2AClient creates an A2A gateway client.
func NewA2AClient(cfg A2AConfig, logger *slog.Logger) *A2AClient {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &A2AClient{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.Timeout},
		logger: logger.With(slog.String("component", "helixon.controlplane.a2a")),
	}
}

// Register announces this agent to the A2A gateway.
func (c *A2AClient) Register(ctx context.Context, card AgentCard) error {
	card.RegisteredAt = time.Now().UTC().Format(time.RFC3339)
	if card.Status == "" {
		card.Status = "active"
	}

	data, _ := json.Marshal(card)
	_, err := c.doPost(ctx, "/api/v1/agents/register", data)
	if err != nil {
		return fmt.Errorf("register agent: %w", err)
	}

	c.logger.Info("registered with A2A gateway",
		slog.String("agent", card.Name),
		slog.String("gateway", c.cfg.GatewayURL),
	)
	return nil
}

// CompleteTask reports task completion to the gateway.
func (c *A2AClient) CompleteTask(ctx context.Context, result TaskResult) error {
	data, _ := json.Marshal(result)
	_, err := c.doPost(ctx, "/api/v1/tasks/complete", data)
	if err != nil {
		return fmt.Errorf("complete task: %w", err)
	}
	return nil
}

// Deregister removes this agent from the gateway.
func (c *A2AClient) Deregister(ctx context.Context, agentID string) error {
	data, _ := json.Marshal(map[string]string{"agent_id": agentID})
	_, err := c.doPost(ctx, "/api/v1/agents/deregister", data)
	return err
}

//nolint:unparam // body is required by the contract even though all current callers pass POST bodies that are discarded; signature stability wins.
func (c *A2AClient) doPost(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.GatewayURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.BearerAuth != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.BearerAuth)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("A2A error %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}
