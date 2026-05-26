package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOllamaClient(t *testing.T) {
	c := NewOllamaClient(Config{BaseURL: "http://localhost:11434", Model: "qwen3"})
	require.NotNil(t, c)
	assert.Equal(t, "http://localhost:11434", c.baseURL)
	assert.Equal(t, "qwen3", c.model)
}

func TestOllamaClient_Complete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req ollamaChatRequest
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, "qwen3", req.Model)
		assert.False(t, req.Stream)

		resp := ollamaChatResponse{
			Model:           "qwen3",
			Message:         Message{Role: "assistant", Content: "Hello from Ollama"},
			Done:            true,
			PromptEvalCount: 50,
			EvalCount:       30,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	resp, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Hello from Ollama", resp.Choices[0].Message.Content)
	assert.Equal(t, 50, resp.Usage.PromptTokens)
	assert.Equal(t, 30, resp.Usage.CompletionTokens)
	assert.Equal(t, 80, resp.Usage.TotalTokens)
}

func TestOllamaClient_Complete_EmptyMessages(t *testing.T) {
	c := NewOllamaClient(Config{BaseURL: "http://localhost:11434", Model: "qwen3"})
	_, err := c.Complete(context.Background(), CompletionRequest{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyMessages)
}

func TestOllamaClient_Complete_ModelOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaChatRequest
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		assert.Equal(t, "custom-model", req.Model)

		resp := ollamaChatResponse{Message: Message{Role: "assistant", Content: "ok"}, Done: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "custom-model",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	require.NoError(t, err)
}

func TestOllamaClient_Complete_DisableThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaChatRequest
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		require.NotNil(t, req.Think)
		assert.False(t, *req.Think)

		resp := ollamaChatResponse{Message: Message{Role: "assistant", Content: "ok"}, Done: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Messages:        []Message{{Role: "user", Content: "Hi"}},
		DisableThinking: true,
	})
	require.NoError(t, err)
}

func TestOllamaClient_Complete_TemperatureAndMaxTokens(t *testing.T) {
	temp := 0.7
	maxTok := 100

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaChatRequest
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		require.NotNil(t, req.Options.Temperature)
		assert.Equal(t, 0.7, *req.Options.Temperature)
		require.NotNil(t, req.Options.NumPredict)
		assert.Equal(t, 100, *req.Options.NumPredict)

		resp := ollamaChatResponse{Message: Message{Role: "assistant", Content: "ok"}, Done: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Messages:    []Message{{Role: "user", Content: "Hi"}},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})
	require.NoError(t, err)
}

func TestOllamaClient_Complete_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.StatusCode)
}

func TestOllamaClient_Complete_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

func TestOllamaClient_Complete_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	require.Error(t, err)
}

func TestOllamaClient_Complete_WithReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaChatResponse{
			Message: Message{Role: "assistant", Content: "answer", Reasoning: "thinking..."},
			Done:    true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewOllamaClient(Config{BaseURL: srv.URL, Model: "qwen3"})
	c.httpClient = srv.Client()

	resp, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "thinking...", resp.Choices[0].Message.Reasoning)
}

func TestAPIError_Error(t *testing.T) {
	e := &APIError{StatusCode: 429, Body: "rate limited"}
	assert.Equal(t, "llm api error (status 429): rate limited", e.Error())
}

func TestClient_DisableThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionAPIRequest
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		require.NotNil(t, req.Think)
		assert.False(t, *req.Think)

		resp := CompletionResponse{
			Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: "ok"}}},
			Usage:   Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "qwen3.5-test"})
	c.httpClient = srv.Client()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Messages:        []Message{{Role: "user", Content: "Hi"}},
		DisableThinking: true,
	})
	require.NoError(t, err)
}

func TestClient_ResponseOversized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		big := bytes.Repeat([]byte("x"), maxResponseSize+100)
		w.Write(big)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "test"})
	c.httpClient = srv.Client()

	_, err := c.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestClient_NewClientTrailingSlash(t *testing.T) {
	c := NewClient(Config{BaseURL: "http://localhost:8080/"})
	assert.Equal(t, "http://localhost:8080", c.baseURL)
}

func TestClient_NewClientWithHTTP(t *testing.T) {
	doer := &http.Client{Timeout: 10 * time.Second}
	c := NewClientWithHTTP(Config{BaseURL: "http://localhost:8080", Model: "test"}, doer)
	require.NotNil(t, c)
	assert.Equal(t, "test", c.model)
}
