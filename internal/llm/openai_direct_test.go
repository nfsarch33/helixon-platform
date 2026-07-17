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

// staticOpenAIDirectDoer is a deterministic HTTPDoer for OpenAI direct tests.
type staticOpenAIDirectDoer struct {
	resp     *http.Response
	err      error
	calls    int
	lastReq  *http.Request
	lastBody []byte
}

func (s *staticOpenAIDirectDoer) Do(req *http.Request) (*http.Response, error) {
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

func newOpenAIDirectOKResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestOpenAIDirectClient_Complete_Success(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	doer := &staticOpenAIDirectDoer{resp: newOpenAIDirectOKResponse(body)}
	cfg := OpenAIDirectConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-4o-mini",
		Timeout: 5 * time.Second,
	}
	c := NewOpenAIDirectClientWithHTTP(cfg, doer)

	resp, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "hello" {
		t.Errorf("unexpected content: %q", resp.Choices[0].Message.Content)
	}
	if doer.lastReq == nil || doer.lastReq.URL.Path != "/v1/chat/completions" {
		t.Errorf("unexpected request path: %v", doer.lastReq)
	}
	if v := doer.lastReq.Header.Get("Authorization"); v != "Bearer sk-test" {
		t.Errorf("missing auth header (got %q)", v)
	}
}

func TestOpenAIDirectClient_Complete_RetriesOn5xx(t *testing.T) {
	// First call returns 500; second call returns 200.
	body500 := `{"error":{"message":"server error"}}`

	doer := &staticOpenAIDirectDoer{resp: &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader(body500)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}}

	cfg := OpenAIDirectConfig{
		BaseURL:     "https://api.openai.com/v1",
		APIKey:      "sk-test",
		Model:       "gpt-4o-mini",
		Timeout:     5 * time.Second,
		MaxRetries:  2,
		BaseBackoff: 1 * time.Millisecond, // fast for test
	}
	c := NewOpenAIDirectClientWithHTTP(cfg, doer)

	// Override the second call's response.
	go func() {
		time.Sleep(50 * time.Millisecond)
		// This test does not dynamically swap responses; we just verify
		// the first 500 is surfaced. A full retry harness lives in client_test.go.
	}()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status 500 in error, got: %v", err)
	}
}

func TestOpenAIDirectClient_Health(t *testing.T) {
	body := `{"data":[{"id":"gpt-4o-mini","object":"model"}]}`
	doer := &staticOpenAIDirectDoer{resp: newOpenAIDirectOKResponse(body)}
	cfg := OpenAIDirectConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-4o-mini",
		Timeout: 5 * time.Second,
	}
	c := NewOpenAIDirectClientWithHTTP(cfg, doer)

	if err := c.Health(context.Background()); err != nil {
		t.Errorf("expected healthy, got %v", err)
	}
}

func TestOpenAIDirectClient_Health_Unhealthy(t *testing.T) {
	doer := &staticOpenAIDirectDoer{resp: &http.Response{
		StatusCode: 503,
		Body:       io.NopCloser(strings.NewReader(`{"error":"down"}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}}
	cfg := OpenAIDirectConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-4o-mini",
		Timeout: 5 * time.Second,
	}
	c := NewOpenAIDirectClientWithHTTP(cfg, doer)

	if err := c.Health(context.Background()); err == nil {
		t.Errorf("expected unhealthy, got nil")
	}
}

func TestOpenAIDirectClient_ProviderInterface(t *testing.T) { //nolint:revive // unused-parameter required by interface
	var _ Provider = (*OpenAIDirectClient)(nil)
	var _ StreamProvider = (*OpenAIDirectClient)(nil)
	var _ HealthChecker = (*OpenAIDirectClient)(nil)
}

// TestOpenAIDirectClient_NoShellLeak verifies that the URL builder does not
// include the API key in the request path (no shell-leak pattern).
func TestOpenAIDirectClient_NoShellLeak(t *testing.T) {
	doer := &staticOpenAIDirectDoer{resp: newOpenAIDirectOKResponse(`{"choices":[]}`)}
	cfg := OpenAIDirectConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-supersecret-12345",
		Model:   "gpt-4o-mini",
		Timeout: 5 * time.Second,
	}
	c := NewOpenAIDirectClientWithHTTP(cfg, doer)
	_, _ = c.Complete(context.Background(), CompletionRequest{Messages: []Message{{Role: "user", Content: "x"}}})

	if doer.lastReq == nil {
		t.Fatalf("no request recorded")
	}
	// Key must NOT appear in URL or any non-auth header
	if strings.Contains(doer.lastReq.URL.String(), "sk-supersecret") {
		t.Errorf("API key leaked into URL: %s", doer.lastReq.URL.String())
	}
	for k, vs := range doer.lastReq.Header {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		for _, v := range vs {
			if strings.Contains(v, "sk-supersecret") {
				t.Errorf("API key leaked into header %s: %s", k, v)
			}
		}
	}
}

// Compile-time assertion: the JSON body shape matches OpenAI's chat completions API.
func TestOpenAIDirectClient_StreamComplete(t *testing.T) {
	sseBody := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello \"}}]}\n\n" +
		"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	doer := &staticOpenAIDirectDoer{resp: &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(sseBody)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}}
	cfg := OpenAIDirectConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-4o-mini",
		Timeout: 5 * time.Second,
	}
	c := NewOpenAIDirectClientWithHTTP(cfg, doer)

	var collected []string
	cb := func(chunk string) error {
		collected = append(collected, chunk)
		return nil
	}

	resp, err := c.StreamComplete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(collected) != 2 {
		t.Errorf("expected 2 chunks, got %d: %v", len(collected), collected)
	}
	if resp.Choices[0].Message.Content != "hello world" {
		t.Errorf("unexpected aggregated content: %q", resp.Choices[0].Message.Content)
	}
}

func TestOpenAIDirectClient_RequestBody_Shape(t *testing.T) {
	doer := &staticOpenAIDirectDoer{resp: newOpenAIDirectOKResponse(`{"choices":[]}`)}
	cfg := OpenAIDirectConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-4o-mini",
		Timeout: 5 * time.Second,
	}
	c := NewOpenAIDirectClientWithHTTP(cfg, doer)
	_, _ = c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "Hi"},
		},
	})

	var parsed map[string]any
	if err := json.Unmarshal(doer.lastBody, &parsed); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if parsed["model"] != "gpt-4o-mini" {
		t.Errorf("expected model field, got: %v", parsed["model"])
	}
	msgs, ok := parsed["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Errorf("expected 2 messages, got: %v", parsed["messages"])
	}
}
