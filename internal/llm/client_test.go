package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeCompletionResponse(content string, promptTokens, completionTokens int) []byte {
	resp := CompletionResponse{
		Choices: []Choice{
			{
				Index:   0,
				Message: Message{Role: "assistant", Content: content},
			},
		},
		Usage: Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req completionAPIRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "test-model", req.Model)
		assert.Len(t, req.Messages, 2)

		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("Hello world", 10, 5))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
	}, srv.Client())

	resp, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "You are a helper."},
			{Role: "user", Content: "Say hello."},
		},
	})

	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Hello world", resp.Choices[0].Message.Content)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
}

func TestComplete_SystemAndUserMessages(t *testing.T) {
	var captured completionAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 5, 2))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Be concise."},
			{Role: "user", Content: "What is 2+2?"},
		},
	})
	require.NoError(t, err)

	require.Len(t, captured.Messages, 2)
	assert.Equal(t, "system", captured.Messages[0].Role)
	assert.Equal(t, "user", captured.Messages[1].Role)
}

func TestComplete_BearerAuth(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "sk-test-key-123",
		Model:   "m",
	}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-test-key-123", authHeader)
}

func TestComplete_NoAuthWhenKeyEmpty(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Empty(t, authHeader)
}

func TestComplete_TemperatureAndMaxTokens(t *testing.T) {
	var captured completionAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	temp := 0.7
	maxTok := 256
	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})
	require.NoError(t, err)
	assert.NotNil(t, captured.Temperature)
	assert.InDelta(t, 0.7, *captured.Temperature, 0.001)
	assert.NotNil(t, captured.MaxTokens)
	assert.Equal(t, 256, *captured.MaxTokens)
}

func TestComplete_RateLimitRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("finally", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	// Override backoff for fast tests
	client.baseBackoff = 1 * time.Millisecond

	resp, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "finally", resp.Choices[0].Message.Content)
	assert.Equal(t, 3, attempts)
}

func TestComplete_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	var apiErr *APIError
	assert.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.StatusCode)
}

func TestComplete_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.Write(fakeCompletionResponse("late", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "context") || strings.Contains(err.Error(), "deadline"))
}

func TestComplete_EmptyMessages(t *testing.T) {
	client := NewClientWithHTTP(Config{BaseURL: "http://unused/v1", Model: "m"}, http.DefaultClient)
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyMessages)
}

func TestComplete_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLLMClient)
}

func TestComplete_UsageTracking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("tracked", 42, 18))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "m"}, srv.Client())
	resp, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, 42, resp.Usage.PromptTokens)
	assert.Equal(t, 18, resp.Usage.CompletionTokens)
	assert.Equal(t, 60, resp.Usage.TotalTokens)
}

func TestComplete_ModelFromConfig(t *testing.T) {
	var captured completionAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{
		BaseURL: srv.URL + "/v1",
		Model:   "qwen3.5:27b",
	}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "qwen3.5:27b", captured.Model)
}

func TestComplete_ModelOverrideInRequest(t *testing.T) {
	var captured completionAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "default-model"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Model:    "override-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "override-model", captured.Model)
}

func TestComplete_QwenModel_SendsThink(t *testing.T) {
	var rawBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&rawBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "qwen3.5-27b"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages:        []Message{{Role: "user", Content: "hi"}},
		DisableThinking: true,
	})
	require.NoError(t, err)
	thinkVal, exists := rawBody["think"]
	assert.True(t, exists, "think param should be present for qwen models")
	assert.Equal(t, false, thinkVal)
}

func TestComplete_OpenAIModel_OmitsThink(t *testing.T) {
	var rawBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&rawBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "gpt-4.1-mini"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages:        []Message{{Role: "user", Content: "hi"}},
		DisableThinking: true,
	})
	require.NoError(t, err)
	_, exists := rawBody["think"]
	assert.False(t, exists, "think param should NOT be present for OpenAI models")
}

func TestComplete_GPT5Model_UsesMaxCompletionTokens(t *testing.T) {
	var rawBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&rawBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeCompletionResponse("ok", 1, 1))
	}))
	defer srv.Close()

	maxTok := 512
	client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: "gpt-5.4"}, srv.Client())
	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: &maxTok,
	})
	require.NoError(t, err)

	_, hasMaxTokens := rawBody["max_tokens"]
	assert.False(t, hasMaxTokens, "max_tokens should NOT be sent for GPT-5 models")

	mctVal, hasMCT := rawBody["max_completion_tokens"]
	assert.True(t, hasMCT, "max_completion_tokens should be present for GPT-5 models")
	assert.InDelta(t, 2048.0, mctVal.(float64), 0.1, "GPT-5 models get 4x token budget for reasoning overhead")
}

func TestComplete_GPT5Model_ScalesTokenBudget(t *testing.T) {
	tests := []struct {
		model    string
		input    int
		expected float64
	}{
		{"gpt-5.4", 1000, 4000},
		{"gpt-5", 256, 1024},
		{"o3", 512, 2048},
		{"o4-mini", 100, 400},
		{"gpt-4.1", 1000, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			var rawBody map[string]interface{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewDecoder(r.Body).Decode(&rawBody)
				w.Header().Set("Content-Type", "application/json")
				w.Write(fakeCompletionResponse("ok", 1, 1))
			}))
			defer srv.Close()

			maxTok := tt.input
			client := NewClientWithHTTP(Config{BaseURL: srv.URL + "/v1", Model: tt.model}, srv.Client())
			_, err := client.Complete(context.Background(), CompletionRequest{
				Messages:  []Message{{Role: "user", Content: "hi"}},
				MaxTokens: &maxTok,
			})
			require.NoError(t, err)

			if isGPT5Model(tt.model) {
				mctVal, hasMCT := rawBody["max_completion_tokens"]
				assert.True(t, hasMCT, "GPT-5/o3/o4 should use max_completion_tokens")
				assert.InDelta(t, tt.expected, mctVal.(float64), 0.1)
				_, hasOld := rawBody["max_tokens"]
				assert.False(t, hasOld, "GPT-5 should NOT send max_tokens")
			} else {
				mtVal, hasMT := rawBody["max_tokens"]
				assert.True(t, hasMT, "non-GPT-5 should use max_tokens")
				assert.InDelta(t, tt.expected, mtVal.(float64), 0.1)
			}
		})
	}
}
