package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentMemory_RetrieveContext_NoSearcher(t *testing.T) {
	am := NewAgentMemory(nil, AgentMemoryConfig{})
	ctx := context.Background()
	result := am.RetrieveContext(ctx, "test query")
	assert.Empty(t, result)
}

func TestAgentMemory_RetrieveContext_WithResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"results": []map[string]any{
				{"id": "m1", "content": "Go uses goroutines for concurrency", "score": 0.9},
				{"id": "m2", "content": "Channels are typed conduits", "score": 0.7},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	searcher := NewHybridSearcher(nil, engram, HybridSearchConfig{MaxResults: 5}, nil)
	am := NewAgentMemory(searcher, AgentMemoryConfig{MaxContext: 2})

	ctx := context.Background()
	result := am.RetrieveContext(ctx, "how does Go handle concurrency")
	assert.Contains(t, result, "relevant_memories")
	assert.Contains(t, result, "Go uses goroutines")
	assert.Contains(t, result, "Channels are typed")
}

func TestAgentMemory_StoreConversationSummary(t *testing.T) {
	var stored string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		msgs, ok := body["messages"].([]interface{}); if ok && len(msgs) > 0 { if m, ok2 := msgs[0].(map[string]interface{}); ok2 { stored, _ = m["content"].(string) } }
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "new-1", "content": stored})
	}))
	defer srv.Close()

	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	searcher := NewHybridSearcher(nil, engram, HybridSearchConfig{}, nil)
	am := NewAgentMemory(searcher, AgentMemoryConfig{})

	ctx := context.Background()
	err := am.StoreConversationSummary(ctx, "User asked about Go concurrency patterns")
	require.NoError(t, err)
	assert.Equal(t, "User asked about Go concurrency patterns", stored)
}

func TestAgentMemory_StoreEmpty(t *testing.T) {
	am := NewAgentMemory(nil, AgentMemoryConfig{})
	err := am.StoreConversationSummary(context.Background(), "")
	assert.NoError(t, err)
}

func TestExtractSummary(t *testing.T) {
	turns := []map[string]string{
		{"role": "user", "content": "How do goroutines work?"},
		{"role": "assistant", "content": "Goroutines are lightweight threads managed by the Go runtime."},
		{"role": "user", "content": "What about channels?"},
		{"role": "assistant", "content": "Channels provide typed communication between goroutines."},
	}
	data, _ := json.Marshal(turns)
	summary := ExtractSummary(data)
	assert.Contains(t, summary, "How do goroutines work?")
	assert.Contains(t, summary, "What about channels?")
	assert.Contains(t, summary, "Goroutines are lightweight")
}

func TestExtractSummary_EmptyTurns(t *testing.T) {
	assert.Empty(t, ExtractSummary([]byte("[]")))
	assert.Empty(t, ExtractSummary([]byte("invalid")))
}
