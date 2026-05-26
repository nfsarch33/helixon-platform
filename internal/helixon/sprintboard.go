package helixon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultSprintboardURL = "http://localhost:9400"
	sprintboardTimeout    = 15 * time.Second
)

// SprintboardClient is a lightweight HTTP client for the Sprintboard
// agent registration and ticket management API at :9400.
type SprintboardClient struct {
	baseURL string
	client  *http.Client
	agentID string
}

// SprintboardConfig configures the sprintboard HTTP client.
type SprintboardConfig struct {
	BaseURL string
	AgentID string
	Timeout time.Duration
}

// NewSprintboardClient creates a client for the Sprintboard REST API.
func NewSprintboardClient(cfg SprintboardConfig) *SprintboardClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultSprintboardURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = sprintboardTimeout
	}
	return &SprintboardClient{
		baseURL: baseURL,
		agentID: cfg.AgentID,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

type registerRequest struct {
	AgentID      string   `json:"agent_id"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Register announces this agent to the Sprintboard, making it visible
// for ticket assignment. Safe to call repeatedly (idempotent).
func (sc *SprintboardClient) Register(ctx context.Context, agentID string) error {
	body := registerRequest{AgentID: agentID}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sprintboard register: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sc.baseURL+"/api/v1/agents/register", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("sprintboard register: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := sc.client.Do(req)
	if err != nil {
		return fmt.Errorf("sprintboard register: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("sprintboard register: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type heartbeatRequest struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

// Heartbeat sends a liveness signal for the agent. Should be called on
// a regular interval (typically every 60s) to prevent the agent from
// being marked as stale.
func (sc *SprintboardClient) Heartbeat(ctx context.Context, agentID string) error {
	body := heartbeatRequest{
		AgentID: agentID,
		Status:  "alive",
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sprintboard heartbeat: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sc.baseURL+"/api/v1/agents/heartbeat", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("sprintboard heartbeat: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := sc.client.Do(req)
	if err != nil {
		return fmt.Errorf("sprintboard heartbeat: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("sprintboard heartbeat: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type claimRequest struct {
	TicketID string `json:"ticket_id"`
	AgentID  string `json:"agent_id"`
}

// ClaimTicket assigns a ticket to the specified agent. Returns an error
// if the ticket is already claimed or does not exist.
func (sc *SprintboardClient) ClaimTicket(ctx context.Context, ticketID, agentID string) error {
	body := claimRequest{
		TicketID: ticketID,
		AgentID:  agentID,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sprintboard claim: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sc.baseURL+"/api/v1/tickets/claim", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("sprintboard claim: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := sc.client.Do(req)
	if err != nil {
		return fmt.Errorf("sprintboard claim: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("sprintboard claim: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
