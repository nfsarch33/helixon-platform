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
type Memory struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	AppID     string    `json:"app_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
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

// Add stores a new memory entry.
func (c *EngramClient) Add(ctx context.Context, content, appID, userID string) (*Memory, error) {
	type engramMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := map[string]any{
		"messages": []engramMsg{{Role: "user", Content: content}},
		"app_id":   appID,
		"user_id":  userID,
	}
	data, _ := json.Marshal(body)

	resp, err := c.doPost(ctx, "/memories", data)
	if err != nil {
		return nil, err
	}

	var mem Memory
	if err := json.Unmarshal(resp, &mem); err != nil {
		return nil, fmt.Errorf("decode add response: %w", err)
	}
	return &mem, nil
}

// Search queries memories by semantic similarity.
func (c *EngramClient) Search(ctx context.Context, query, appID, userID string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	body := map[string]any{
		"query":   query,
		"app_id":  appID,
		"user_id": userID,
		"limit":   limit,
	}
	data, _ := json.Marshal(body)

	resp, err := c.doPost(ctx, "/search", data)
	if err != nil {
		return nil, err
	}

	var wrapper struct {
		Results []SearchResult `json:"results"`
	}
	if err := json.Unmarshal(resp, &wrapper); err != nil {
		var results []SearchResult
		if err2 := json.Unmarshal(resp, &results); err2 != nil {
			return nil, fmt.Errorf("decode search response: %w", err)
		}
		return results, nil
	}
	return wrapper.Results, nil
}

// Get retrieves a specific memory by ID.
func (c *EngramClient) Get(ctx context.Context, id string) (*Memory, error) {
	resp, err := c.doGet(ctx, "/memories/"+id)
	if err != nil {
		return nil, err
	}
	var mem Memory
	if err := json.Unmarshal(resp, &mem); err != nil {
		return nil, fmt.Errorf("decode get response: %w", err)
	}
	return &mem, nil
}

// Health checks the Engram server status.
func (c *EngramClient) Health(ctx context.Context) error {
	_, err := c.doGet(ctx, "/health")
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
			lastErr = fmt.Errorf("%w: %s", ErrEngramUnavailable, err)
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
		return nil, fmt.Errorf("%w: %s", ErrEngramUnavailable, err)
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
