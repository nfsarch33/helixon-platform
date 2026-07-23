// runx-public-repo-gate: allow-file fleet_host_alias
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
	defaultMem0BaseURL = "http://localhost:18888"
	mem0Timeout        = 10 * time.Second
)

// Memory represents a single memory entry returned from Mem0.
type Memory struct {
	ID        string            `json:"id"`
	Content   string            `json:"memory"`
	CreatedAt string            `json:"created_at,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	Score     float64           `json:"score,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// MemoryTool provides HTTP access to a Mem0 instance for agent memory
// search and storage operations.
type MemoryTool struct {
	baseURL string
	client  *http.Client
	agentID string
}

// MemoryToolConfig configures the Mem0 HTTP client.
type MemoryToolConfig struct {
	BaseURL string
	AgentID string
	Timeout time.Duration
}

// NewMemoryTool creates a MemoryTool pointing at the given Mem0 instance.
func NewMemoryTool(cfg MemoryToolConfig) *MemoryTool {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultMem0BaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = mem0Timeout
	}
	return &MemoryTool{
		baseURL: baseURL,
		agentID: cfg.AgentID,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// mem0SearchRequest is the POST body for /v1/memories/search.
type mem0SearchRequest struct {
	Query   string `json:"query"`
	AgentID string `json:"agent_id,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// mem0AddRequest is the POST body for /v1/memories/.
type mem0AddRequest struct {
	Messages []mem0Message `json:"messages"`
	AgentID  string        `json:"agent_id,omitempty"`
	Infer    bool          `json:"infer"`
}

type mem0Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Search queries Mem0 for memories matching the given query string.
func (mt *MemoryTool) Search(ctx context.Context, query string) ([]Memory, error) {
	body := mem0SearchRequest{
		Query:   query,
		AgentID: mt.agentID,
		Limit:   10,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("mem0 search: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mt.baseURL+"/v1/memories/search/", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mem0 search: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := mt.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mem0 search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("mem0 search: status %d: %s", resp.StatusCode, string(respBody))
	}

	var memories []Memory
	if err := json.NewDecoder(resp.Body).Decode(&memories); err != nil {
		return nil, fmt.Errorf("mem0 search: decode: %w", err)
	}
	return memories, nil
}

// Add stores a new memory in Mem0. When infer is true, Mem0 extracts
// facts from the content automatically; when false, the content is
// stored verbatim.
func (mt *MemoryTool) Add(ctx context.Context, content string, infer bool) error {
	body := mem0AddRequest{
		Messages: []mem0Message{
			{Role: "user", Content: content},
		},
		AgentID: mt.agentID,
		Infer:   infer,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("mem0 add: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mt.baseURL+"/v1/memories/", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("mem0 add: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := mt.client.Do(req)
	if err != nil {
		return fmt.Errorf("mem0 add: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("mem0 add: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ToolName returns the registry name for this tool.
func (mt *MemoryTool) ToolName() string { return "memory" }
