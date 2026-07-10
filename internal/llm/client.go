package llm

import (
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

const (
	maxRetries      = 3
	maxResponseSize = 5 * 1024 * 1024 // 5 MB
	defaultBackoff  = 500 * time.Millisecond

	// reasoningModelMultiplier scales max_completion_tokens for reasoning models
	// (GPT-5.x, o3, o4) that consume tokens on internal chain-of-thought before
	// producing visible output.
	reasoningModelMultiplier = 4
)

// Sentinel errors for LLM client operations.
var (
	ErrLLMClient     = errors.New("llm client error")
	ErrEmptyMessages = errors.New("messages must not be empty")
	ErrLLMTimeout    = errors.New("llm request timed out")
	ErrLLMRateLimit  = errors.New("llm rate limit exceeded")
)

// APIError represents a non-2xx HTTP response from the LLM provider.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm api error (status %d): %s", e.StatusCode, e.Body)
}

// HTTPDoer abstracts HTTP execution for testing.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config holds LLM client configuration.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// Message represents a chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// CompletionRequest is the public request type for callers.
type CompletionRequest struct {
	Model           string
	Messages        []Message
	Temperature     *float64
	MaxTokens       *int
	DisableThinking bool
	Tools           []Tool      `json:"tools,omitempty"`
	ToolChoice      *ToolChoice `json:"tool_choice,omitempty"`
}

// CompletionResponse is the parsed response from the LLM provider.
type CompletionResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a single completion choice.
type Choice struct {
	Index   int     `json:"index"`
	Message Message `json:"message"`
}

// Usage tracks token consumption.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// completionAPIRequest is the wire format sent to the OpenAI-compatible API.
type completionAPIRequest struct {
	Model               string      `json:"model"`
	Messages            []Message   `json:"messages"`
	Temperature         *float64    `json:"temperature,omitempty"`
	MaxTokens           *int        `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int        `json:"max_completion_tokens,omitempty"`
	Think               *bool       `json:"think,omitempty"`
	Tools               []Tool      `json:"tools,omitempty"`
	ToolChoice          *ToolChoice `json:"tool_choice,omitempty"`
}

// isQwenModel returns true for Qwen-family models that support the think parameter.
func isQwenModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "qwen")
}

// isGPT5Model returns true for GPT-5.x models that require max_completion_tokens
// instead of the deprecated max_tokens parameter.
func isGPT5Model(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "gpt-5") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4")
}

// Provider defines the interface for LLM operations.
type Provider interface {
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

// Client implements Provider using an OpenAI-compatible chat completions API.
type Client struct {
	baseURL     string
	apiKey      string
	model       string
	httpClient  HTTPDoer
	baseBackoff time.Duration
}

// NewClient creates a Provider from config with a default HTTP client.
func NewClient(cfg Config) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		baseBackoff: defaultBackoff,
	}
}

// NewClientWithHTTP creates a Provider with an injected HTTP doer (for testing).
func NewClientWithHTTP(cfg Config, doer HTTPDoer) *Client {
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		httpClient:  doer,
		baseBackoff: defaultBackoff,
	}
}

// Complete sends a chat completion request and returns the response.
func (c *Client) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}

	model := c.model
	if req.Model != "" {
		model = req.Model
	}

	apiReq := completionAPIRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	}

	if isGPT5Model(model) && req.MaxTokens != nil {
		scaled := *req.MaxTokens * reasoningModelMultiplier
		apiReq.MaxCompletionTokens = &scaled
	} else {
		apiReq.MaxTokens = req.MaxTokens
	}

	if req.DisableThinking && isQwenModel(model) {
		f := false
		apiReq.Think = &f
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := c.baseBackoff * time.Duration(1<<(attempt-1))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err := c.doRequest(ctx, apiReq)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
				lastErr = err
				continue
			}
			return nil, err
		}
		return resp, nil
	}

	return nil, lastErr
}

func (c *Client) doRequest(ctx context.Context, apiReq completionAPIRequest) (*CompletionResponse, error) {
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request: %w", ErrLLMClient, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrLLMClient, err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLLMClient, err)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseSize+1)))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %w", ErrLLMClient, err)
	}

	if len(respBody) > maxResponseSize {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrLLMClient, maxResponseSize)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var result CompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("%w: decode response: %w", ErrLLMClient, err)
	}

	return &result, nil
}

// OllamaClient implements Provider using Ollama's native /api/chat endpoint,
// which supports the think parameter for controlling reasoning-mode models.
type OllamaClient struct {
	baseURL     string
	model       string
	httpClient  HTTPDoer
	baseBackoff time.Duration
}

// NewOllamaClient creates a Provider targeting Ollama's native API.
func NewOllamaClient(cfg Config) *OllamaClient {
	return &OllamaClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		baseBackoff: defaultBackoff,
	}
}

type ollamaChatRequest struct {
	Model    string        `json:"model"`
	Messages []Message     `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  ollamaOptions `json:"options,omitempty"`
	Think    *bool         `json:"think,omitempty"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

type ollamaChatResponse struct {
	Model           string  `json:"model"`
	Message         Message `json:"message"`
	Done            bool    `json:"done"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
}

// Complete sends a chat request via the Ollama native API.
func (c *OllamaClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}

	model := c.model
	if req.Model != "" {
		model = req.Model
	}

	apiReq := ollamaChatRequest{
		Model:    model,
		Messages: req.Messages,
		Stream:   false,
		Options: ollamaOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxTokens,
		},
	}
	if req.DisableThinking {
		f := false
		apiReq.Think = &f
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request: %w", ErrLLMClient, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrLLMClient, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLLMClient, err)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseSize+1)))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %w", ErrLLMClient, err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var result ollamaChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("%w: decode response: %w", ErrLLMClient, err)
	}

	return &CompletionResponse{
		Choices: []Choice{
			{Index: 0, Message: result.Message},
		},
		Usage: Usage{
			PromptTokens:     result.PromptEvalCount,
			CompletionTokens: result.EvalCount,
			TotalTokens:      result.PromptEvalCount + result.EvalCount,
		},
	}, nil
}
