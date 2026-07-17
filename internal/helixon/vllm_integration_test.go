package helixon_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

func TestVLLMProviderE2E(t *testing.T) {
	baseURL := "http://127.0.0.1:8000/v1"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/models")
	if err != nil || resp.StatusCode != 200 {
		t.Skipf("vLLM not available at %s (err=%v)", baseURL, err)
	}
	_ = resp.Body.Close()

	cfg := helixon.ProviderConfig{
		Kind:    "openai-compat",
		BaseURL: baseURL,
		Model:   "Qwen/Qwen3.5-4B",
		APIKey:  "not-needed",
	}
	provider, err := helixon.BuildProvider(cfg)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := provider.Complete(ctx, llm.CompletionRequest{
		Messages: []llm.Message{{Role: "user", Content: "What is 2+2? Answer with just the number."}},
	})
	if err != nil {
		t.Skipf("vLLM completion failed (GPU may be blocked): %v", err)
	}
	if len(result.Choices) == 0 {
		t.Fatal("no choices in response")
	}
	t.Logf("vLLM response: %s", result.Choices[0].Message.Content)
	if result.Choices[0].Message.Content == "" {
		t.Fatal("empty response content from vLLM")
	}
	t.Logf("Usage: prompt=%d completion=%d total=%d",
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
}
