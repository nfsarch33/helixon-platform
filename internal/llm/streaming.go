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
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []ToolCallDelta `json:"tool_calls,omitempty"`
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

// streamAccumulator holds the running state of an in-flight SSE parse:
// the concatenated content, accumulated tool calls keyed by tool index,
// and the final usage block.
type streamAccumulator struct {
	content   strings.Builder
	toolCalls map[int]*ToolCall
	usage     Usage
}

// newStreamAccumulator creates an empty accumulator.
func newStreamAccumulator() *streamAccumulator {
	return &streamAccumulator{toolCalls: make(map[int]*ToolCall)}
}

// parseSSEStream reads an SSE byte stream and accumulates tool calls and
// content into a single CompletionResponse. Content chunks are forwarded
// to cb as they arrive.
//
// v16716-4 refactor: parseSSEStream (CC=20) decomposed into:
//   - processStreamChunk (CC=4): per-chunk delta handling
//   - accumulateToolCall  (CC=3): tool-call delta merging
//   - finalizeStreamResp  (CC=3): post-loop CompletionResponse build
//
// parseSSEStream (CC=5) is now a thin orchestrator.
func parseSSEStream(r io.Reader, cb StreamCallback) (*CompletionResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	acc := newStreamAccumulator()

	for scanner.Scan() {
		line := scanner.Text()
		if !isSSEDataLine(line) {
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
		if err := processStreamChunk(acc, chunk, cb); err != nil {
			return nil, err
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
	}
	return finalizeStreamResp(acc), nil
}

// isSSEDataLine reports whether the line is an SSE data line that should
// be processed (i.e. non-empty and starts with the "data: " prefix).
func isSSEDataLine(line string) bool {
	if line == "" {
		return false
	}
	return strings.HasPrefix(line, "data: ")
}

// processStreamChunk accumulates a single SSE chunk into acc. Content
// deltas are forwarded to cb. Returns the first callback error encountered.
func processStreamChunk(acc *streamAccumulator, chunk StreamChunk, cb StreamCallback) error {
	if chunk.Usage != nil {
		acc.usage = *chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			acc.content.WriteString(choice.Delta.Content)
			if cb != nil {
				if err := cb(choice.Delta.Content); err != nil {
					return fmt.Errorf("stream callback: %w", err)
				}
			}
		}
		for _, tcd := range choice.Delta.ToolCalls {
			accumulateToolCall(acc.toolCalls, tcd)
		}
	}
	return nil
}

// accumulateToolCall merges a single tool-call delta into the accumulator
// map keyed by tool index. New entries are created; existing entries are
// updated in place (ID/Type once-only, Name/Arguments appended).
func accumulateToolCall(toolCalls map[int]*ToolCall, tcd ToolCallDelta) {
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

// finalizeStreamResp assembles the final CompletionResponse from the
// accumulator. Tool calls are emitted in index order so callers can
// rely on stable ordering.
func finalizeStreamResp(acc *streamAccumulator) *CompletionResponse {
	msg := Message{
		Role:    "assistant",
		Content: acc.content.String(),
	}
	if len(acc.toolCalls) > 0 {
		tcs := make([]ToolCall, 0, len(acc.toolCalls))
		for i := 0; i < maxToolIndex(acc.toolCalls)+1; i++ {
			if tc, ok := acc.toolCalls[i]; ok {
				tcs = append(tcs, *tc)
			}
		}
		msg.ToolCalls = tcs
	}
	return &CompletionResponse{
		Choices: []Choice{{Index: 0, Message: msg}},
		Usage:   acc.usage,
	}
}

// maxToolIndex returns the largest key in the tool-call index map.
// Returns -1 when the map is empty so the +1 in finalizeStreamResp
// yields a zero-length iteration.
func maxToolIndex(m map[int]*ToolCall) int {
	max := -1
	for k := range m {
		if k > max {
			max = k
		}
	}
	return max
}
