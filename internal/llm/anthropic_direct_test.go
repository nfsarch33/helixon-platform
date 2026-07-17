package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// anthropicMessagesRequest mirrors the Anthropic /v1/messages wire format.
//
//nolint:unused // documented wire-format reference
type anthropicMessagesRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	MaxTokens int       `json:"max_tokens"`
}

// anthropicMessagesResponse mirrors the Anthropic /v1/messages response.
//
//nolint:unused // documented wire-format reference
type anthropicMessagesResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      Usage  `json:"usage"`
}

// staticAnthropicDirectDoer is the Anthropic equivalent of staticOpenAIDirectDoer.
type staticAnthropicDirectDoer struct {
	resp     *http.Response
	err      error
	calls    int
	lastReq  *http.Request
	lastBody []byte
}

func (s *staticAnthropicDirectDoer) Do(req *http.Request) (*http.Response, error) {
	s.calls++
	s.lastReq = req
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		s.lastBody = body
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func newAnthropicOKResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestAnthropicDirectClient_Complete_Success(t *testing.T) {
	body := `{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"hello-anthropic"}],"stop_reason":"end_turn","usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	doer := &staticAnthropicDirectDoer{resp: newAnthropicOKResponse(body)}
	cfg := AnthropicDirectConfig{
		BaseURL: "https://api.anthropic.com",
		APIKey:  "sk-ant-test",
		Model:   "claude-haiku-4-5",
		Timeout: 5 * time.Second,
	}
	c := NewAnthropicDirectClientWithHTTP(cfg, doer)

	resp, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "hello-anthropic" {
		t.Errorf("unexpected content: %q", resp.Choices[0].Message.Content)
	}
	if doer.lastReq.Header.Get("x-api-key") != "sk-ant-test" {
		t.Errorf("missing x-api-key header")
	}
	if doer.lastReq.Header.Get("anthropic-version") == "" {
		t.Errorf("missing anthropic-version header")
	}
	if doer.lastReq.URL.Path != "/v1/messages" {
		t.Errorf("unexpected request path: %s", doer.lastReq.URL.Path)
	}
}

func TestAnthropicDirectClient_Complete_Health(t *testing.T) {
	doer := &staticAnthropicDirectDoer{resp: newAnthropicOKResponse(`{"id":"msg_01"}`)}
	cfg := AnthropicDirectConfig{
		BaseURL: "https://api.anthropic.com",
		APIKey:  "sk-ant-test",
		Model:   "claude-haiku-4-5",
		Timeout: 5 * time.Second,
	}
	c := NewAnthropicDirectClientWithHTTP(cfg, doer)
	if err := c.Health(context.Background()); err != nil {
		t.Errorf("expected healthy, got %v", err)
	}
}

func TestAnthropicDirectClient_Complete_HealthFail(t *testing.T) {
	doer := &staticAnthropicDirectDoer{resp: &http.Response{
		StatusCode: 401,
		Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
	}}
	cfg := AnthropicDirectConfig{
		BaseURL: "https://api.anthropic.com",
		APIKey:  "sk-ant-test",
		Model:   "claude-haiku-4-5",
		Timeout: 5 * time.Second,
	}
	c := NewAnthropicDirectClientWithHTTP(cfg, doer)
	if err := c.Health(context.Background()); err == nil {
		t.Errorf("expected unhealthy, got nil")
	}
}

func TestAnthropicDirectClient_ProviderInterface(t *testing.T) { //nolint:revive // unused-parameter required by interface
	var _ Provider = (*AnthropicDirectClient)(nil)
	var _ HealthChecker = (*AnthropicDirectClient)(nil)
}

func TestAnthropicDirectClient_NoShellLeak(t *testing.T) {
	doer := &staticAnthropicDirectDoer{resp: newAnthropicOKResponse(`{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"x"}]}`)}
	cfg := AnthropicDirectConfig{
		BaseURL: "https://api.anthropic.com",
		APIKey:  "sk-ant-supersecret-12345",
		Model:   "claude-haiku-4-5",
		Timeout: 5 * time.Second,
	}
	c := NewAnthropicDirectClientWithHTTP(cfg, doer)
	_, _ = c.Complete(context.Background(), CompletionRequest{Messages: []Message{{Role: "user", Content: "x"}}})

	if doer.lastReq == nil {
		t.Fatalf("no request recorded")
	}
	if strings.Contains(doer.lastReq.URL.String(), "sk-ant-supersecret") {
		t.Errorf("API key leaked into URL")
	}
	for k, vs := range doer.lastReq.Header {
		if strings.EqualFold(k, "x-api-key") {
			continue
		}
		for _, v := range vs {
			if strings.Contains(v, "sk-ant-supersecret") {
				t.Errorf("API key leaked into header %s: %s", k, v)
			}
		}
	}
}

func TestAnthropicDirectClient_ExtractSystemMessage(t *testing.T) {
	doer := &staticAnthropicDirectDoer{resp: newAnthropicOKResponse(`{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"x"}]}`)}
	cfg := AnthropicDirectConfig{
		BaseURL: "https://api.anthropic.com",
		APIKey:  "sk-ant-test",
		Model:   "claude-haiku-4-5",
		Timeout: 5 * time.Second,
	}
	c := NewAnthropicDirectClientWithHTTP(cfg, doer)
	_, _ = c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "You are concise"},
			{Role: "user", Content: "Hi"},
		},
	})
	var parsed map[string]any
	if err := json.Unmarshal(doer.lastBody, &parsed); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if parsed["system"] != "You are concise" {
		t.Errorf("expected system field, got %v", parsed["system"])
	}
	msgs, ok := parsed["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Errorf("expected 1 non-system message, got %v", parsed["messages"])
	}
}
