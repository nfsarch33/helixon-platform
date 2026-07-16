package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Mem0Result is the slim shape produced by any Mem0Client implementation.
// It mirrors the subset of fields HybridSearcher needs for federated search
// and rank-merging.
//
// Deprecated: Mem0 retired per ADR-063; Engram is the PRIMARY memory engine.
// The federated-search interface is preserved for in-package symmetry; no
// production caller wires Mem0Client after v18654. Removal targeted v18656+.
// See CF-2026-07-16-122.
type Mem0Result struct {
	ID      string  `json:"id"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Mem0Client federates Mem0 alongside Engram + FTS5 inside HybridSearcher.
// Implementations must be safe for concurrent use.
//
// Deprecated: see Mem0Result. CF-2026-07-16-122.
type Mem0Client interface {
	Search(ctx context.Context, query string, limit int) ([]Mem0Result, error)
	Add(ctx context.Context, content string) error
}

// Mem0HTTPConfig configures the production HTTP adapter used in the runtime.
type Mem0HTTPConfig struct {
	BaseURL string
	AgentID string
	Timeout time.Duration
}

func (c Mem0HTTPConfig) withDefaults() Mem0HTTPConfig {
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:18888"
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	return c
}

// Mem0HTTPClient talks to a Mem0 server over HTTP. It is the production
// implementation of Mem0Client.
type Mem0HTTPClient struct {
	cfg  Mem0HTTPConfig
	http *http.Client
}

// NewMem0HTTPClient returns a Mem0Client that proxies to a real Mem0 server.
func NewMem0HTTPClient(cfg Mem0HTTPConfig) *Mem0HTTPClient {
	cfg = cfg.withDefaults()
	return &Mem0HTTPClient{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
}

type mem0SearchBody struct {
	Query   string `json:"query"`
	AgentID string `json:"agent_id,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type mem0AddBody struct {
	Messages []mem0Msg `json:"messages"`
	AgentID  string    `json:"agent_id,omitempty"`
	Infer    bool      `json:"infer"`
}

type mem0Msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// rawMem0Result accepts both `memory` and `content` in the Mem0 API response.
type rawMem0Result struct {
	ID      string  `json:"id"`
	Memory  string  `json:"memory"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Search implements Mem0Client.
func (c *Mem0HTTPClient) Search(ctx context.Context, query string, limit int) ([]Mem0Result, error) {
	if limit <= 0 {
		limit = 10
	}
	body := mem0SearchBody{Query: query, AgentID: c.cfg.AgentID, Limit: limit}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("mem0 search marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/v1/memories/search/", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mem0 search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mem0 search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("mem0 search status %d: %s", resp.StatusCode, string(raw))
	}
	var raw []rawMem0Result
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("mem0 search decode: %w", err)
	}
	out := make([]Mem0Result, 0, len(raw))
	for _, r := range raw {
		content := r.Memory
		if content == "" {
			content = r.Content
		}
		out = append(out, Mem0Result{ID: r.ID, Content: content, Score: r.Score})
	}
	return out, nil
}

// Add implements Mem0Client.
func (c *Mem0HTTPClient) Add(ctx context.Context, content string) error {
	body := mem0AddBody{
		Messages: []mem0Msg{{Role: "user", Content: content}},
		AgentID:  c.cfg.AgentID,
		Infer:    false,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("mem0 add marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/v1/memories/", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("mem0 add request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mem0 add: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("mem0 add status %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}
