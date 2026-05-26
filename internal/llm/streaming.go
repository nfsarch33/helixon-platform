package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// StreamChunk represents a single SSE chunk from the streaming API.
type StreamChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Choices []StreamDelta `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

// StreamDelta represents a choice delta in a streaming response.
type StreamDelta struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// Delta is the incremental content in a streaming response.
type Delta struct {
	Role      string              `json:"role,omitempty"`
	Content   string              `json:"content,omitempty"`
	ToolCalls []ToolCallDelta     `json:"tool_calls,omitempty"`
}

// ToolCallDelta is the streaming-specific tool call with an index for
// accumulation across multiple SSE chunks.
type ToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// StreamCallback is invoked for each content chunk during streaming.
type StreamCallback func(chunk string) error

// StreamProvider extends Provider with streaming support.
type StreamProvider interface {
	Provider
	StreamComplete(ctx context.Context, req CompletionRequest, cb StreamCallback) (*CompletionResponse, error)
}

// StreamComplete sends a streaming chat completion request and invokes
// cb for each content chunk. It accumulates the full response (including
// tool calls) and returns it as a CompletionResponse for the agent loop.
func (c *Client) StreamComplete(ctx context.Context, req CompletionRequest, cb StreamCallback) (*CompletionResponse, error) {
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

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request: %s", ErrLLMClient, err)
	}

	streamBody, err := addStreamFlag(body)
	if err != nil {
		return nil, fmt.Errorf("%w: add stream flag: %s", ErrLLMClient, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(streamBody))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %s", ErrLLMClient, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrLLMClient, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	return parseSSEStream(resp.Body, cb)
}

func addStreamFlag(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["stream"] = json.RawMessage("true")
	return json.Marshal(m)
}

// parseSSEStream reads an SSE byte stream and accumulates tool calls and
// content into a single CompletionResponse. Content chunks are forwarded
// to cb as they arrive.
func parseSSEStream(r io.Reader, cb StreamCallback) (*CompletionResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		fullContent strings.Builder
		toolCalls   = make(map[int]*ToolCall)
		totalUsage  Usage
	)

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			totalUsage = *chunk.Usage
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				fullContent.WriteString(choice.Delta.Content)
				if cb != nil {
					if err := cb(choice.Delta.Content); err != nil {
						return nil, fmt.Errorf("stream callback: %w", err)
					}
				}
			}

			for _, tcd := range choice.Delta.ToolCalls {
				existing, ok := toolCalls[tcd.Index]
				if !ok {
					existing = &ToolCall{
						ID:   tcd.ID,
						Type: tcd.Type,
						Function: FunctionCall{
							Name: tcd.Function.Name,
						},
					}
					toolCalls[tcd.Index] = existing
				} else {
					if tcd.ID != "" {
						existing.ID = tcd.ID
					}
					if tcd.Type != "" {
						existing.Type = tcd.Type
					}
					if tcd.Function.Name != "" {
						existing.Function.Name += tcd.Function.Name
					}
				}
				existing.Function.Arguments += tcd.Function.Arguments
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
	}

	msg := Message{
		Role:    "assistant",
		Content: fullContent.String(),
	}

	if len(toolCalls) > 0 {
		tcs := make([]ToolCall, 0, len(toolCalls))
		for i := 0; i < len(toolCalls); i++ {
			if tc, ok := toolCalls[i]; ok {
				tcs = append(tcs, *tc)
			}
		}
		msg.ToolCalls = tcs
	}

	return &CompletionResponse{
		Choices: []Choice{{Index: 0, Message: msg}},
		Usage:   totalUsage,
	}, nil
}
