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

// BedrockConfig holds configuration for a Bedrock-proxied Claude client.
type BedrockConfig struct {
	BaseURL string
	APIKey  string
	ModelID string
	Timeout time.Duration
}

// BedrockClient implements Provider using the Anthropic Bedrock Messages API,
// proxied through the Zendesk AI gateway at /bedrock/model/{model_id}/invoke.
type BedrockClient struct {
	baseURL     string
	apiKey      string
	modelID     string
	httpClient  HTTPDoer
	baseBackoff time.Duration
}

// NewBedrockClient creates a Provider targeting the Bedrock gateway.
func NewBedrockClient(cfg BedrockConfig) *BedrockClient {
	return &BedrockClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		modelID: cfg.ModelID,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		baseBackoff: defaultBackoff,
	}
}

// NewBedrockClientWithHTTP creates a BedrockClient with an injected HTTP doer.
func NewBedrockClientWithHTTP(cfg BedrockConfig, doer HTTPDoer) *BedrockClient {
	return &BedrockClient{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		modelID:     cfg.ModelID,
		httpClient:  doer,
		baseBackoff: defaultBackoff,
	}
}

type bedrockMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type bedrockRequest struct {
	AnthropicVersion string           `json:"anthropic_version"`
	MaxTokens        int              `json:"max_tokens"`
	System           string           `json:"system,omitempty"`
	Messages         []bedrockMessage `json:"messages"`
	Temperature      *float64         `json:"temperature,omitempty"`
}

type bedrockContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type bedrockUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type bedrockResponse struct {
	ID         string                `json:"id"`
	Model      string                `json:"model"`
	Content    []bedrockContentBlock `json:"content"`
	StopReason string                `json:"stop_reason"`
	Usage      bedrockUsage          `json:"usage"`
}

// Complete sends a chat request via the Bedrock Messages API.
func (c *BedrockClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}

	modelID := c.modelID
	if req.Model != "" {
		modelID = req.Model
	}

	maxTok := 4096
	if req.MaxTokens != nil {
		maxTok = *req.MaxTokens
	}

	var systemPrompt string
	var msgs []bedrockMessage
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}
		msgs = append(msgs, bedrockMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	apiReq := bedrockRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTok,
		System:           systemPrompt,
		Messages:         msgs,
		Temperature:      req.Temperature,
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

		resp, err := c.doRequest(ctx, modelID, apiReq)
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

func (c *BedrockClient) doRequest(ctx context.Context, modelID string, apiReq bedrockRequest) (*CompletionResponse, error) {
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request: %s", ErrLLMClient, err)
	}

	url := c.baseURL + "/model/" + modelID + "/invoke"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %s", ErrLLMClient, err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrLLMClient, err)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseSize+1)))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %s", ErrLLMClient, err)
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

	var result bedrockResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("%w: decode response: %s", ErrLLMClient, err)
	}

	var content string
	for _, block := range result.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	return &CompletionResponse{
		Choices: []Choice{
			{Index: 0, Message: Message{Role: "assistant", Content: content}},
		},
		Usage: Usage{
			PromptTokens:     result.Usage.InputTokens,
			CompletionTokens: result.Usage.OutputTokens,
			TotalTokens:      result.Usage.InputTokens + result.Usage.OutputTokens,
		},
	}, nil
}

// IsBedrockEndpoint returns true if the URL path suggests a Bedrock gateway.
func IsBedrockEndpoint(url string) bool {
	return strings.Contains(url, "/bedrock")
}

// IsClaudeModel returns true for Anthropic Claude model identifiers.
func IsClaudeModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "claude") || strings.Contains(lower, "anthropic")
}
