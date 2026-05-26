package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AgentInfo describes a registered agent and its current workload.
type AgentInfo struct {
	AgentID      string `json:"agent_id"`
	Status       string `json:"status"`
	CurrentTask  string `json:"current_task,omitempty"`
	Capabilities string `json:"capabilities,omitempty"`
	LastSeen     string `json:"last_seen,omitempty"`
}

// AgentWorkloadResponse is the JSON payload for /api/v1/agents.
type AgentWorkloadResponse struct {
	Agents      []AgentInfo `json:"agents"`
	TotalAgents int         `json:"total_agents"`
	ActiveTasks int         `json:"active_tasks"`
	GeneratedAt string      `json:"generated_at"`
}

// AgentWorkloadFetcher queries SprintBoard for active agents and their tickets.
type AgentWorkloadFetcher struct {
	sprintboardURL string
	client         *http.Client
}

// NewAgentWorkloadFetcher creates a fetcher that queries the SprintBoard API.
func NewAgentWorkloadFetcher(sprintboardURL string) *AgentWorkloadFetcher {
	if sprintboardURL == "" {
		sprintboardURL = "http://localhost:8585"
	}
	return &AgentWorkloadFetcher{
		sprintboardURL: sprintboardURL,
		client:         &http.Client{Timeout: 5 * time.Second},
	}
}

// Fetch retrieves the current agent workload from SprintBoard.
func (f *AgentWorkloadFetcher) Fetch(ctx context.Context) (*AgentWorkloadResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.sprintboardURL+"/api/v1/agents", nil)
	if err != nil {
		return nil, fmt.Errorf("agent workload: build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent workload: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("agent workload: read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("agent workload: status %d: %s", resp.StatusCode, string(data))
	}

	var agents []AgentInfo
	if err := json.Unmarshal(data, &agents); err != nil {
		var wrapper struct {
			Agents []AgentInfo `json:"agents"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			return nil, fmt.Errorf("agent workload: decode: %w", err)
		}
		agents = wrapper.Agents
	}

	activeTasks := 0
	for _, a := range agents {
		if a.CurrentTask != "" {
			activeTasks++
		}
	}

	return &AgentWorkloadResponse{
		Agents:      agents,
		TotalAgents: len(agents),
		ActiveTasks: activeTasks,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// AgentWorkloadHandler returns an HTTP handler for /api/v1/agents.
func AgentWorkloadHandler(fetcher *AgentWorkloadFetcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		resp, err := fetcher.Fetch(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("fetch error: %v", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
