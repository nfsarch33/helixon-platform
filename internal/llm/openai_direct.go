// Package llm provides direct (non-Bedrock, non-Eino) adapters for OpenAI and
// Anthropic chat-completion APIs. These adapters are used in production paths
// where the Helixon Agent needs to talk to personal OpenAI/Anthropic API keys
// without going through the corporate Bedrock proxy.
//
// Adapters implement the same Provider, StreamProvider, and HealthChecker
// interfaces as the existing Bedrock and OpenAI-compatible Client so they
// are drop-in replacements.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIDirectConfig configures an OpenAI direct API client.
type OpenAIDirectConfig struct {
	BaseURL     string        // e.g. "https://api.openai.com/v1"
	APIKey      string        // personal OpenAI API key
	Model       string        // e.g. "gpt-4o-mini"
	Timeout     time.Duration // per-request timeout
	MaxRetries  int           // default 3
	BaseBackoff time.Duration // default 500ms
}

// OpenAIDirectClient is a Provider implementation that talks to OpenAI's
// chat completions API directly (without Bedrock or any proxy).
type OpenAIDirectClient struct {
	baseURL     string
	apiKey      string
	model       string
	httpClient  HTTPDoer
	maxRetries  int
	baseBackoff time.Duration
}

// NewOpenAIDirectClient creates a client with a default HTTP client.
func NewOpenAIDirectClient(cfg OpenAIDirectConfig) *OpenAIDirectClient {
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = maxRetriesConst
	}
	baseBackoff := cfg.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultBackoff
	}
	return &OpenAIDirectClient{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		httpClient:  &http.Client{Timeout: cfg.Timeout},
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
	}
}

// NewOpenAIDirectClientWithHTTP creates a client with an injected HTTPDoer (for tests).
func NewOpenAIDirectClientWithHTTP(cfg OpenAIDirectConfig, doer HTTPDoer) *OpenAIDirectClient {
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = maxRetriesConst
	}
	baseBackoff := cfg.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultBackoff
	}
	return &OpenAIDirectClient{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		httpClient:  doer,
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
	}
}

// completeAPIRequest mirrors the OpenAI chat completions wire format.
type openAICompleteRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

// Complete performs a non-streaming chat completion against OpenAI.
func (c *OpenAIDirectClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}
	model := c.model
	if req.Model != "" {
		model = req.Model
	}
	apiReq := openAICompleteRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	// Idempotency-Key per AGENTS.md §9.1: not yet wired through CompletionRequest.
	// Future: extend CompletionRequest with IdempotencyKey and forward here.

	resp, err := c.executeWithRetry(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var out CompletionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	return &out, nil
}

// StreamComplete performs a streaming chat completion (SSE) and invokes the
// callback for each content chunk.
func (c *OpenAIDirectClient) StreamComplete(ctx context.Context, req CompletionRequest, cb StreamCallback) (*CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}
	model := c.model
	if req.Model != "" {
		model = req.Model
	}
	apiReq := openAICompleteRequest{
		Model:    model,
		Messages: req.Messages,
		Stream:   true,
	}
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal openai stream request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(errBody)}
	}

	return parseOpenAISSEStream(resp.Body, cb)
}

// Health checks OpenAI by listing models (cheap API call).
func (c *OpenAIDirectClient) Health(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("openai health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		return fmt.Errorf("openai unhealthy status=%d body=%s", resp.StatusCode, string(body))
	}
	return nil
}

// executeWithRetry retries transient 5xx/network errors per AGENTS.md §8.3
// (max 3 attempts, exponential backoff with jitter). 4xx errors fail fast.
func (c *OpenAIDirectClient) executeWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		// Re-read body for each retry because http.NewRequestWithContext consumes it.
		var bodyBytes []byte
		if req.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("read body: %w", err)
			}
		}

		retryReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL.String(), bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("build retry request: %w", err)
		}
		retryReq.Header = req.Header.Clone()

		resp, err := c.httpClient.Do(retryReq)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries-1 {
				backoff(c.baseBackoff, attempt)
				continue
			}
			return nil, fmt.Errorf("openai request exhausted retries: %w", lastErr)
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = &APIError{StatusCode: resp.StatusCode}
			if attempt < c.maxRetries-1 {
				backoff(c.baseBackoff, attempt)
				continue
			}
			return nil, lastErr
		}
		return resp, nil
	}
	return nil, lastErr
}

const maxRetriesConst = 3

// backoff sleeps for base * 2^attempt with a small jitter.
func backoff(base time.Duration, attempt int) {
	d := base << attempt
	// Simple jitter: 80-120% of d
	pct := int64(80 + (attempt*7)%40)
	jitter := time.Duration(int64(d) * pct / 100)
	time.Sleep(jitter)
}

// parseOpenAISSEStream consumes an SSE response from OpenAI and invokes the
// callback for each content chunk. Returns the aggregated CompletionResponse.
func parseOpenAISSEStream(r io.Reader, cb StreamCallback) (*CompletionResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxResponseSize)
	agg := &CompletionResponse{}
	var contentBuf strings.Builder

	for scanner.Scan() {
		event := scanner.Text()
		if !strings.HasPrefix(event, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(event, "data: ")
		if payload == "[DONE]" {
			break
		}
		if err := processSSEPayload(payload, &contentBuf, cb); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("sse scan: %w", err)
	}

	// Synthesize a single Choice to mirror Complete() output.
	agg.Choices = []Choice{{
		Index:   0,
		Message: Message{Role: "assistant", Content: contentBuf.String()},
	}}
	return agg, nil
}

// processSSEPayload unmarshals one SSE chunk and dispatches any content to cb.
func processSSEPayload(payload string, buf *strings.Builder, cb StreamCallback) error {
	var chunk StreamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		// skip malformed chunk; do not abort the stream
		//nolint:nilerr // intentional: malformed SSE is recoverable
		return nil
	}
	for _, delta := range chunk.Choices {
		content := delta.Delta.Content
		if content == "" {
			continue
		}
		buf.WriteString(content)
		if cb != nil {
			if err := cb(content); err != nil {
				return fmt.Errorf("stream callback: %w", err)
			}
		}
	}
	return nil
}

// HealthChecker is implemented by adapters that can perform a lightweight
// health probe against the upstream API.
type HealthChecker interface {
	Health(ctx context.Context) error
}
