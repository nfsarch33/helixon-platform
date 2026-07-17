package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamComplete_ContentChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []string{
			`{"id":"1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
			`{"id":"1","choices":[{"index":0,"delta":{"content":" world"}}]}`,
			`{"id":"1","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer func() { srv.Close() }()

	client := NewClient(Config{BaseURL: srv.URL, Model: "test-model"})

	var received []string
	resp, err := client.StreamComplete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Say hello"}},
	}, func(chunk string) error {
		received = append(received, chunk)
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, "Hello world!", resp.Choices[0].Message.Content)
	assert.Equal(t, []string{"Hello", " world", "!"}, received)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 3, resp.Usage.CompletionTokens)
}

func TestStreamComplete_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []string{
			`{"id":"1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":""}}]}}]}`,
			`{"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}`,
			`{"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\",\"args\":[\"/tmp\"]}"}}]}}]}`,
			`{"id":"1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":15}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer func() { srv.Close() }()

	client := NewClient(Config{BaseURL: srv.URL, Model: "test-model"})

	resp, err := client.StreamComplete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "list files"}},
		Tools:    []Tool{{Type: "function", Function: FunctionDef{Name: "shell"}}},
	}, nil)
	require.NoError(t, err)

	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	tc := resp.Choices[0].Message.ToolCalls[0]
	assert.Equal(t, "call_1", tc.ID)
	assert.Equal(t, "shell", tc.Function.Name)
	assert.Equal(t, `{"command":"ls","args":["/tmp"]}`, tc.Function.Arguments)
}

func TestStreamComplete_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer func() { srv.Close() }()

	client := NewClient(Config{BaseURL: srv.URL, Model: "test-model"})

	_, err := client.StreamComplete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

func TestParseSSEStream_EmptyMessages(t *testing.T) {
	client := NewClient(Config{BaseURL: "http://localhost", Model: "test"})
	_, err := client.StreamComplete(context.Background(), CompletionRequest{}, nil)
	assert.ErrorIs(t, err, ErrEmptyMessages)
}

func TestParseSSEStream_MalformedJSON(t *testing.T) {
	body := strings.NewReader("data: {invalid json}\n\ndata: [DONE]\n\n")
	resp, err := parseSSEStream(body, nil)
	require.NoError(t, err)
	assert.Equal(t, "", resp.Choices[0].Message.Content)
}
