// Package memory hosts the Helixon agent memory layer (Engram client + agent helpers).
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

var (
	ErrEngramUnavailable = errors.New("engram server unavailable")
	ErrMemoryNotFound    = errors.New("memory not found")
)

// EngramConfig configures the HTTP client for the Engram memory service.
type EngramConfig struct {
	BaseURL    string
	Timeout    time.Duration
	MaxRetries int
}

func (c EngramConfig) withDefaults() EngramConfig {
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:8787"
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 2
	}
	return c
}

// Memory represents a stored memory entry from Engram.
//
// TenantID isolates memory entries across tenants. An empty TenantID
// marks a legacy entry (pre-multi-tenancy) and is visible to all tenant
// filters for backward compatibility — see v18680-3 semantics. A
// non-empty TenantID is enforced by every Search/Recall call to
// prevent cross-tenant leaks.
type Memory struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	AppID     string    `json:"app_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	TenantID  string    `json:"tenant_id,omitempty"`
	Score     float64   `json:"score,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// SearchResult is a scored memory from a search query.
type SearchResult struct {
	Memory
	Score float64 `json:"score"`
}

// EngramClient talks to the Engram memory service over HTTP.
type EngramClient struct {
	baseURL string
	http    *http.Client
	retries int
	logger  *slog.Logger
}

// NewEngramClient creates a client for the Engram memory service.
func NewEngramClient(cfg EngramConfig, logger *slog.Logger) *EngramClient {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &EngramClient{
		baseURL: cfg.BaseURL,
		http:    &http.Client{Timeout: cfg.Timeout},
		retries: cfg.MaxRetries,
		logger:  logger.With(slog.String("component", "helixon.memory.engram")),
	}
}

// engramRecord is the wire shape of a memory record in the canonical
// Engram daemon API (v2 httpapi): text instead of content, and tenant
// scope carried as workspace_id. Responses are mapped back to Memory.
type engramRecord struct {
	ID          string    `json:"id"`
	Text        string    `json:"text"`
	UserID      string    `json:"user_id"`
	AppID       string    `json:"app_id"`
	WorkspaceID string    `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func (r engramRecord) toMemory() Memory {
	return Memory{
		ID:        r.ID,
		Content:   r.Text,
		AppID:     r.AppID,
		UserID:    r.UserID,
		TenantID:  r.WorkspaceID,
		CreatedAt: r.CreatedAt,
	}
}

// Add stores a new memory entry. tenantID stamps the entry with the
// tenant scope (sent as workspace_id on the wire); pass "" for legacy /
// pre-migration callers. The daemon expects messages as plain strings
// and returns the created records as a JSON array.
func (c *EngramClient) Add(ctx context.Context, content, appID, userID, tenantID string) (*Memory, error) {
	body := map[string]any{
		"messages":     []string{content},
		"app_id":       appID,
		"user_id":      userID,
		"workspace_id": tenantID,
		"infer":        false,
	}
	data, _ := json.Marshal(body)

	resp, err := c.doPost(ctx, "/memories", data)
	if err != nil {
		return nil, err
	}

	var recs []engramRecord
	if err := json.Unmarshal(resp, &recs); err != nil {
		return nil, fmt.Errorf("decode add response: %w", err)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("engram add returned no records")
	}
	mem := recs[0].toMemory()
	return &mem, nil
}

// Search queries memories by semantic similarity. tenantID filters
// results by tenant per v18684-4 multi-tenancy hardening (sent as
// workspace_id on the wire); an empty tenantID matches all tenants.
// The daemon expects top_k and returns a JSON array of {record, score}.
func (c *EngramClient) Search(ctx context.Context, query, appID, userID, tenantID string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	body := map[string]any{
		"query":        query,
		"app_id":       appID,
		"user_id":      userID,
		"workspace_id": tenantID,
		"top_k":        limit,
	}
	data, _ := json.Marshal(body)

	resp, err := c.doPost(ctx, "/search", data)
	if err != nil {
		return nil, err
	}

	var wire []struct {
		Record engramRecord `json:"record"`
		Score  float64      `json:"score"`
	}
	if err := json.Unmarshal(resp, &wire); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	results := make([]SearchResult, 0, len(wire))
	for _, hit := range wire {
		results = append(results, SearchResult{Memory: hit.Record.toMemory(), Score: hit.Score})
	}
	return results, nil
}

// Get retrieves a specific memory by ID.
func (c *EngramClient) Get(ctx context.Context, id string) (*Memory, error) {
	resp, err := c.doGet(ctx, "/memories/"+id)
	if err != nil {
		return nil, err
	}
	var rec engramRecord
	if err := json.Unmarshal(resp, &rec); err != nil {
		return nil, fmt.Errorf("decode get response: %w", err)
	}
	mem := rec.toMemory()
	return &mem, nil
}

// Delete removes a memory by ID.
func (c *EngramClient) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/memories/"+id, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEngramUnavailable, err)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return ErrMemoryNotFound
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("engram error: %d %s", resp.StatusCode, string(data))
	}
	return nil
}

// Health checks the Engram server status.
func (c *EngramClient) Health(ctx context.Context) error {
	_, err := c.doGet(ctx, "/healthz")
	return err
}

func (c *EngramClient) doPost(ctx context.Context, path string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*200) * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: %w", ErrEngramUnavailable, err)
			continue
		}

		data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read response: %w", err)
			continue
		}

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("engram server error: %d %s", resp.StatusCode, string(data))
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("engram client error: %d %s", resp.StatusCode, string(data))
		}

		return data, nil
	}
	return nil, lastErr
}

func (c *EngramClient) doGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEngramUnavailable, err)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrMemoryNotFound
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("engram error: %d %s", resp.StatusCode, string(data))
	}
	return data, nil
}
