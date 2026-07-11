package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicDirectConfig configures an Anthropic direct API client.
type AnthropicDirectConfig struct {
	BaseURL     string        // e.g. "https://api.anthropic.com"
	APIKey      string        // personal Anthropic API key (sk-ant-...)
	Model       string        // e.g. "claude-haiku-4-5"
	APIVersion  string        // default "2023-06-01"
	Timeout     time.Duration // per-request timeout
	MaxRetries  int           // default 3
	BaseBackoff time.Duration // default 500ms
}

// AnthropicDirectClient is a Provider implementation that talks to Anthropic's
// /v1/messages API directly (without Bedrock or any proxy).
//
// Note: Anthropic's API shape differs from OpenAI's:
//   - Endpoint: POST /v1/messages (not /chat/completions)
//   - Auth header: x-api-key (not Authorization: Bearer)
//   - Required header: anthropic-version
//   - System messages are a separate top-level field, not in messages[]
//   - max_tokens is REQUIRED (no default)
type AnthropicDirectClient struct {
	baseURL          string
	apiKey           string
	model            string
	apiVersion       string
	httpClient       HTTPDoer
	maxRetries       int
	baseBackoff      time.Duration
	defaultMaxTokens int
}

// NewAnthropicDirectClient creates a client with a default HTTP client.
func NewAnthropicDirectClient(cfg AnthropicDirectConfig) *AnthropicDirectClient {
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	baseBackoff := cfg.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultBackoff
	}
	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = "2023-06-01"
	}
	return &AnthropicDirectClient{
		baseURL:          strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:           cfg.APIKey,
		model:            cfg.Model,
		apiVersion:       apiVersion,
		httpClient:       &http.Client{Timeout: cfg.Timeout},
		maxRetries:       maxRetries,
		baseBackoff:      baseBackoff,
		defaultMaxTokens: 4096,
	}
}

// NewAnthropicDirectClientWithHTTP creates a client with an injected HTTPDoer.
func NewAnthropicDirectClientWithHTTP(cfg AnthropicDirectConfig, doer HTTPDoer) *AnthropicDirectClient {
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	baseBackoff := cfg.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultBackoff
	}
	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = "2023-06-01"
	}
	return &AnthropicDirectClient{
		baseURL:          strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:           cfg.APIKey,
		model:            cfg.Model,
		apiVersion:       apiVersion,
		httpClient:       doer,
		maxRetries:       maxRetries,
		baseBackoff:      baseBackoff,
		defaultMaxTokens: 4096,
	}
}

// anthropicRequest is the wire format for POST /v1/messages.
type anthropicRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream,omitempty"`
}

// anthropicResponseContent is one element in the content[] array.
type anthropicResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicResponse is the response shape from /v1/messages.
type anthropicResponse struct {
	ID         string                     `json:"id"`
	Type       string                     `json:"type"`
	Role       string                     `json:"role"`
	Content    []anthropicResponseContent `json:"content"`
	StopReason string                     `json:"stop_reason"`
	Usage      Usage                      `json:"usage"`
}

// Complete performs a non-streaming chat completion against Anthropic.
func (c *AnthropicDirectClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}
	model := c.resolveModel(req.Model)

	// Extract system messages into the top-level "system" field.
	systemPrompt, convMessages := extractAnthropicSystem(req.Messages)
	maxTokens := c.resolveMaxTokens(req.MaxTokens)

	apiReq := anthropicRequest{
		Model:     model,
		Messages:  convMessages,
		System:    systemPrompt,
		MaxTokens: maxTokens,
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	httpReq, err := c.buildRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, err := c.executeWithRetry(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(errBody)}
	}

	var out anthropicResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}

	// Translate Anthropic response shape → CompletionResponse.
	text := extractAnthropicText(out.Content)
	return &CompletionResponse{
		Choices: []Choice{{
			Index:   0,
			Message: Message{Role: "assistant", Content: text},
		}},
		Usage: out.Usage,
	}, nil
}

// extractAnthropicText concatenates all text blocks from Anthropic content array.
func extractAnthropicText(blocks []anthropicResponseContent) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// extractAnthropicSystem pulls the "system" role messages out of a request and
// concatenates them with blank lines; non-system messages are returned in order.
func extractAnthropicSystem(msgs []Message) (string, []Message) {
	var systemPrompt string
	convMessages := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n\n"
			}
			systemPrompt += m.Content
			continue
		}
		convMessages = append(convMessages, m)
	}
	return systemPrompt, convMessages
}

// resolveModel returns the request override or the configured default.
func (c *AnthropicDirectClient) resolveModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	return c.model
}

// resolveMaxTokens returns the request override or the configured default.
func (c *AnthropicDirectClient) resolveMaxTokens(reqMax *int) int {
	if reqMax != nil {
		return *reqMax
	}
	return c.defaultMaxTokens
}

// buildRequest assembles the POST /v1/messages HTTP request with auth headers.
func (c *AnthropicDirectClient) buildRequest(ctx context.Context, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", c.apiVersion)
	httpReq.Header.Set("Content-Type", "application/json")
	return httpReq, nil
}

// Health checks Anthropic by issuing a minimal /v1/messages call with max_tokens=1.
func (c *AnthropicDirectClient) Health(ctx context.Context) error {
	body, _ := json.Marshal(anthropicRequest{
		Model:     c.model,
		Messages:  []Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", c.apiVersion)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("anthropic health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		return fmt.Errorf("anthropic unhealthy status=%d body=%s", resp.StatusCode, string(errBody))
	}
	return nil
}

// executeWithRetry mirrors OpenAIDirectClient.executeWithRetry but is a
// per-client method so backoff state can differ per provider.
func (c *AnthropicDirectClient) executeWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
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
			return nil, fmt.Errorf("anthropic request exhausted retries: %w", lastErr)
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
