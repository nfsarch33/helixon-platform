package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeBedrockResponse(text string, inputTokens, outputTokens int) []byte {
	resp := bedrockResponse{
		ID:    "msg_test_123",
		Model: "claude-opus-4-20250514",
		Content: []bedrockContentBlock{
			{Type: "text", Text: text},
		},
		StopReason: "end_turn",
		Usage: bedrockUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

func TestBedrockClient_Complete_Success(t *testing.T) {
	var capturedReq bedrockRequest
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		json.NewDecoder(r.Body).Decode(&capturedReq)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeBedrockResponse("Hello from Claude!", 15, 8))
	}))
	defer srv.Close()

	client := NewBedrockClientWithHTTP(BedrockConfig{
		BaseURL: srv.URL + "/bedrock",
		APIKey:  "test-key",
		ModelID: "us.anthropic.claude-opus-4-20250514-v1:0",
	}, srv.Client())

	temp := 0.5
	maxTok := 1024
	resp, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "You are a helper."},
			{Role: "user", Content: "Say hello."},
		},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})

	require.NoError(t, err)
	assert.Equal(t, "/bedrock/model/us.anthropic.claude-opus-4-20250514-v1:0/invoke", capturedPath)
	assert.Equal(t, "bedrock-2023-05-31", capturedReq.AnthropicVersion)
	assert.Equal(t, "You are a helper.", capturedReq.System)
	assert.Len(t, capturedReq.Messages, 1)
	assert.Equal(t, "user", capturedReq.Messages[0].Role)
	assert.Equal(t, 1024, capturedReq.MaxTokens)

	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Hello from Claude!", resp.Choices[0].Message.Content)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
}

func TestBedrockClient_Complete_MapsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeBedrockResponse("tracked", 42, 18))
	}))
	defer srv.Close()

	client := NewBedrockClientWithHTTP(BedrockConfig{
		BaseURL: srv.URL + "/bedrock",
		ModelID: "claude-test",
	}, srv.Client())

	resp, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	require.NoError(t, err)
	assert.Equal(t, 42, resp.Usage.PromptTokens)
	assert.Equal(t, 18, resp.Usage.CompletionTokens)
	assert.Equal(t, 60, resp.Usage.TotalTokens)
}

func TestBedrockClient_Complete_EmptyMessages(t *testing.T) {
	client := NewBedrockClientWithHTTP(BedrockConfig{
		BaseURL: "http://unused/bedrock",
		ModelID: "test",
	}, http.DefaultClient)

	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyMessages)
}

func TestIsBedrockEndpoint(t *testing.T) {
	assert.True(t, IsBedrockEndpoint("https://ai-gateway.zende.sk/bedrock"))
	assert.True(t, IsBedrockEndpoint("http://localhost:8080/bedrock/model/x/invoke"))
	assert.False(t, IsBedrockEndpoint("https://ai-gateway.zende.sk/v1"))
}

func TestIsClaudeModel(t *testing.T) {
	assert.True(t, IsClaudeModel("us.anthropic.claude-opus-4-20250514-v1:0"))
	assert.True(t, IsClaudeModel("claude-sonnet-4-20250514"))
	assert.False(t, IsClaudeModel("gpt-5.4"))
	assert.False(t, IsClaudeModel("qwen3.5-27b"))
}
