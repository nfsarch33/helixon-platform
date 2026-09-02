package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func TestEngramClientAdd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/memories", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)

		// The canonical daemon expects messages as plain strings; a
		// mem0-style {role,content} object would fail this decode with
		// 400, exactly like the live daemon.
		var req struct {
			Messages    []string `json:"messages"`
			AppID       string   `json:"app_id"`
			WorkspaceID string   `json:"workspace_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		assert.Equal(t, []string{"test memory"}, req.Messages)
		assert.Equal(t, "helixon", req.AppID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode([]engramRecord{{
			ID:    "mem-001",
			Text:  "test memory",
			AppID: "helixon",
		}})
	}))
	defer func() { srv.Close() }()

	client := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	mem, err := client.Add(context.Background(), "test memory", "helixon", "user-1", "")
	require.NoError(t, err)
	assert.Equal(t, "mem-001", mem.ID)
	assert.Equal(t, "test memory", mem.Content)
}

func TestEngramClientSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/search", r.URL.Path)

		var req struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "k8s", req.Query)
		assert.Equal(t, 10, req.TopK, "daemon reads top_k, not limit")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"record": engramRecord{ID: "m1", Text: "kubernetes deployment"}, "score": 0.95},
			{"record": engramRecord{ID: "m2", Text: "docker container"}, "score": 0.80},
		})
	}))
	defer func() { srv.Close() }()

	client := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	results, err := client.Search(context.Background(), "k8s", "app", "user", "", 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, "kubernetes deployment", results[0].Content)
	assert.InDelta(t, 0.95, results[0].Score, 0.001)
}

func TestEngramClientHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/healthz", r.URL.Path, "daemon serves /healthz, not /health")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer func() { srv.Close() }()

	client := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	err := client.Health(context.Background())
	assert.NoError(t, err)
}

func TestEngramClientRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode([]engramRecord{{ID: "retry-ok"}})
	}))
	defer func() { srv.Close() }()

	client := NewEngramClient(EngramConfig{BaseURL: srv.URL, MaxRetries: 3}, nil)
	mem, err := client.Add(context.Background(), "will retry", "app", "user", "")
	require.NoError(t, err)
	assert.Equal(t, "retry-ok", mem.ID)
	assert.Equal(t, 3, attempts)
}

func TestWorkspaceInjector(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace")
	inj, err := NewWorkspaceInjector(dir)
	require.NoError(t, err)

	err = inj.WriteAGENTSMD(WorkspaceConfig{
		AgentName:    "TestBot",
		Capabilities: []string{"search", "code review", "deploy"},
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")) //nolint:gosec // G304 test fixture
	require.NoError(t, err)
	assert.Contains(t, string(data), "TestBot Agent")
	assert.Contains(t, string(data), "- search")
	assert.Contains(t, string(data), "Operating Constraints")

	err = inj.WriteSOULMD("TestBot", nil)
	require.NoError(t, err)
	data, err = os.ReadFile(filepath.Join(dir, "SOUL.md")) //nolint:gosec // G304 test fixture
	require.NoError(t, err)
	assert.Contains(t, string(data), "Personality Traits")

	err = inj.WriteUSERMD("user-123", map[string]string{"language": "Go", "style": "TDD"})
	require.NoError(t, err)
	data, err = os.ReadFile(filepath.Join(dir, "USER.md")) //nolint:gosec // G304 test fixture
	require.NoError(t, err)
	assert.Contains(t, string(data), "user-123")
	assert.Contains(t, string(data), "Go")
}

func TestWorkspaceReadAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace")
	inj, err := NewWorkspaceInjector(dir)
	require.NoError(t, err)

	_ = inj.WriteAGENTSMD(WorkspaceConfig{AgentName: "Bot"})
	_ = inj.WriteSOULMD("Bot", nil)

	content, err := inj.ReadAll()
	require.NoError(t, err)
	assert.Contains(t, content, "--- AGENTS.md ---")
	assert.Contains(t, content, "--- SOUL.md ---")
	assert.Contains(t, content, "Bot Agent")
}

func TestHybridSearcherFTSOnly(t *testing.T) {
	db := openTempSQLite(t)

	searcher := NewHybridSearcher(db, nil, HybridSearchConfig{}, nil)
	err := searcher.EnsureSchema(context.Background())
	require.NoError(t, err)

	err = searcher.IndexLocal(context.Background(), "doc1", "kubernetes pod scheduling algorithms")
	require.NoError(t, err)
	err = searcher.IndexLocal(context.Background(), "doc2", "docker compose networking guide")
	require.NoError(t, err)
	err = searcher.IndexLocal(context.Background(), "doc3", "kubernetes service mesh with istio")
	require.NoError(t, err)

	results, err := searcher.Search(context.Background(), "kubernetes", "", "", "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	for _, r := range results {
		assert.Contains(t, r.Content, "kubernetes")
		assert.Equal(t, "fts5", r.Source)
	}
}

func TestHybridSearcherVectorOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"record": engramRecord{ID: "v1", Text: "vector result alpha"}, "score": 0.9},
			{"record": engramRecord{ID: "v2", Text: "vector result beta"}, "score": 0.7},
		})
	}))
	defer func() { srv.Close() }()

	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	searcher := NewHybridSearcher(nil, engram, HybridSearchConfig{}, nil)

	results, err := searcher.Search(context.Background(), "test query", "app", "user", "")
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, "engram", results[0].Source)
}

func TestHybridSearcherMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"record": engramRecord{ID: "v1", Text: "shared content"}, "score": 0.8},
			{"record": engramRecord{ID: "v2", Text: "vector-only content"}, "score": 0.6},
		})
	}))
	defer func() { srv.Close() }()

	db := openTempSQLite(t)

	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	searcher := NewHybridSearcher(db, engram, HybridSearchConfig{
		FTSWeight:    0.3,
		VectorWeight: 0.7,
	}, nil)
	err := searcher.EnsureSchema(context.Background())
	require.NoError(t, err)

	_ = searcher.IndexLocal(context.Background(), "l1", "shared content")
	_ = searcher.IndexLocal(context.Background(), "l2", "local-only content")

	results, err := searcher.Search(context.Background(), "shared content", "app", "user", "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	hasHybrid := false
	for _, r := range results {
		if r.Source == "hybrid" && r.Content == "shared content" {
			hasHybrid = true
		}
	}
	assert.True(t, hasHybrid, "expected merged result with source='hybrid'")
}

// TestHybridSearcherWrite_DualWriteCanonicalAsymmetry locks the canonical
// vs secondary write contract: a green Engram write returns the persisted
// Memory and mirrors into the local FTS index; a failed local mirror logs
// but does not propagate; a failed Engram write fails the whole call.
func TestHybridSearcherWrite_GreenPathMirrorsLocal(t *testing.T) {
	var posted map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/memories":
			_ = json.NewDecoder(r.Body).Decode(&posted)
			contentStr := ""
			if msgs, ok := posted["messages"].([]interface{}); ok && len(msgs) > 0 {
				contentStr, _ = msgs[0].(string)
			}
			appID, _ := posted["app_id"].(string)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode([]engramRecord{{
				ID:    "mem-write-001",
				Text:  contentStr,
				AppID: appID,
			}})
		case "/search":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer func() { srv.Close() }()

	db := openTempSQLite(t)

	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	searcher := NewHybridSearcher(db, engram, HybridSearchConfig{}, nil)
	require.NoError(t, searcher.EnsureSchema(context.Background()))

	mem, err := searcher.Write(context.Background(), "v8000-overnight memory", "claude-code", "user-1", "")
	require.NoError(t, err)
	require.NotNil(t, mem)
	assert.Equal(t, "mem-write-001", mem.ID)
	assert.Equal(t, "v8000-overnight memory", mem.Content)
	msgs, _ := posted["messages"].([]interface{})
	require.Len(t, msgs, 1)
	assert.Equal(t, "v8000-overnight memory", msgs[0], "messages must be plain strings on the wire")
	assert.Equal(t, "claude-code", posted["app_id"])

	// FTS mirror landed locally.
	results, err := searcher.Search(context.Background(), "overnight", "claude-code", "user-1", "")
	require.NoError(t, err)
	found := false
	for _, r := range results {
		if r.ID == "mem-write-001" && r.Source == "fts5" {
			found = true
		}
	}
	assert.True(t, found, "local FTS mirror should be searchable")
}

func TestHybridSearcherWrite_CanonicalFailureBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "engram down", http.StatusServiceUnavailable)
	}))
	defer func() { srv.Close() }()

	db := openTempSQLite(t)

	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL, MaxRetries: 1}, nil)
	searcher := NewHybridSearcher(db, engram, HybridSearchConfig{}, nil)
	require.NoError(t, searcher.EnsureSchema(context.Background()))

	mem, err := searcher.Write(context.Background(), "should fail", "claude-code", "", "")
	require.Error(t, err)
	assert.Nil(t, mem)
}

func TestHybridSearcherWrite_MissingEngramFails(t *testing.T) {
	searcher := NewHybridSearcher(nil, nil, HybridSearchConfig{}, nil)
	_, err := searcher.Write(context.Background(), "x", "app", "user", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "engram client not configured")
}

func TestHybridSearcherRead_GreenPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/memories/mem-read-001", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engramRecord{ID: "mem-read-001", Text: "fetched"})
	}))
	defer func() { srv.Close() }()

	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	searcher := NewHybridSearcher(nil, engram, HybridSearchConfig{}, nil)

	mem, err := searcher.Read(context.Background(), "mem-read-001")
	require.NoError(t, err)
	require.NotNil(t, mem)
	assert.Equal(t, "fetched", mem.Content)
}

func TestHybridSearcherRead_EmptyIDFails(t *testing.T) {
	engram := NewEngramClient(EngramConfig{BaseURL: "http://127.0.0.1:1"}, nil)
	searcher := NewHybridSearcher(nil, engram, HybridSearchConfig{}, nil)
	_, err := searcher.Read(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")
}
