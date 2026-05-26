package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// stubMem0 is a minimal in-memory Mem0Client used by the federation tests.
// Production callers wire a real HTTP-backed implementation.
type stubMem0 struct {
	mu       sync.Mutex
	added    []string
	results  []Mem0Result
	addErr   error
	searchErr error
}

func (s *stubMem0) Search(_ context.Context, _ string, _ int) ([]Mem0Result, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.results, nil
}

func (s *stubMem0) Add(_ context.Context, content string) error {
	if s.addErr != nil {
		return s.addErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added = append(s.added, content)
	return nil
}

func (s *stubMem0) addedCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.added))
	copy(out, s.added)
	return out
}

func newEngramServerEcho(t *testing.T) (*httptest.Server, *EngramClient) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/memories":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Memory{
				ID:      "engram-001",
				Content: body["content"],
				AppID:   body["app_id"],
			})
		case "/api/v1/memories/search":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Results []SearchResult `json:"results"`
			}{
				Results: []SearchResult{
					{Memory: Memory{ID: "v1", Content: "shared content"}, Score: 0.8},
					{Memory: Memory{ID: "v2", Content: "engram-only result"}, Score: 0.6},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
}

func TestHybridSearcher_FederatedSearch_MergesAllThreeSources(t *testing.T) {
	srv, engram := newEngramServerEcho(t)
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "fed.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	mem0 := &stubMem0{
		results: []Mem0Result{
			{ID: "m0-1", Content: "shared content", Score: 0.9},
			{ID: "m0-2", Content: "mem0-only insight", Score: 0.5},
		},
	}

	searcher := NewHybridSearcher(db, engram, HybridSearchConfig{}, nil)
	searcher.WithMem0(mem0)
	require.NoError(t, searcher.EnsureSchema(context.Background()))

	require.NoError(t, searcher.IndexLocal(context.Background(), "lf1", "shared content"))
	require.NoError(t, searcher.IndexLocal(context.Background(), "lf2", "fts-only memo"))

	results, err := searcher.Search(context.Background(), "shared content", "claude-code", "user-1")
	require.NoError(t, err)
	require.NotEmpty(t, results)

	contents := map[string]string{}
	for _, r := range results {
		contents[r.Content] = r.Source
	}

	// Each backend must contribute its exclusive entry, and the consensus
	// "shared content" must collapse into a single hybrid-ranked entry.
	assert.Contains(t, contents, "shared content", "shared content must merge into one entry")
	assert.Contains(t, contents, "engram-only result", "engram-only entry must be present")
	assert.Contains(t, contents, "mem0-only insight", "mem0-only entry must be present")

	assert.Equal(t, "hybrid", contents["shared content"], "consensus entry must be tagged hybrid")
	assert.Equal(t, "shared content", results[0].Content, "consensus content must rank first")
}

func TestHybridSearcher_FederatedSearch_Mem0FailureNonFatal(t *testing.T) {
	srv, engram := newEngramServerEcho(t)
	defer srv.Close()

	mem0 := &stubMem0{searchErr: errors.New("mem0 down")}

	searcher := NewHybridSearcher(nil, engram, HybridSearchConfig{}, nil)
	searcher.WithMem0(mem0)

	results, err := searcher.Search(context.Background(), "shared content", "claude-code", "user-1")
	require.NoError(t, err, "mem0 failure must not fail the federated call")
	assert.NotEmpty(t, results, "engram results must still be returned")
}

func TestHybridSearcher_FederatedWrite_Mem0Mirror(t *testing.T) {
	srv, engram := newEngramServerEcho(t)
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "fed-write.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	mem0 := &stubMem0{}
	searcher := NewHybridSearcher(db, engram, HybridSearchConfig{}, nil)
	searcher.WithMem0(mem0)
	require.NoError(t, searcher.EnsureSchema(context.Background()))

	mem, err := searcher.Write(context.Background(), "v8800 federated memory", "claude-code", "user-1")
	require.NoError(t, err)
	require.NotNil(t, mem)

	added := mem0.addedCopy()
	require.Len(t, added, 1, "mem0 must receive the secondary mirror")
	assert.Equal(t, "v8800 federated memory", added[0])
}

func TestHybridSearcher_FederatedWrite_Mem0FailureNonFatal(t *testing.T) {
	srv, engram := newEngramServerEcho(t)
	defer srv.Close()

	mem0 := &stubMem0{addErr: errors.New("mem0 add boom")}
	searcher := NewHybridSearcher(nil, engram, HybridSearchConfig{}, nil)
	searcher.WithMem0(mem0)

	mem, err := searcher.Write(context.Background(), "still ok", "claude-code", "user-1")
	require.NoError(t, err, "mem0 mirror failure must not fail canonical write")
	require.NotNil(t, mem)
}

// TestMem0HTTPClient_Adapter exercises the production Mem0 HTTP adapter
// against an httptest server so we lock the wire contract used at runtime.
func TestMem0HTTPClient_Adapter(t *testing.T) {
	var (
		searchHits int
		addHits    int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/memories/search/":
			searchHits++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "mem0-a", "memory": "kubernetes", "score": 0.91},
			})
		case "/v1/memories/":
			addHits++
			body, _ := readAllBody(r)
			assert.Contains(t, string(body), `"role":"user"`)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"mem0-add"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewMem0HTTPClient(Mem0HTTPConfig{
		BaseURL: srv.URL,
		AgentID: "claude-code",
	})

	results, err := c.Search(context.Background(), "kubernetes", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "kubernetes", results[0].Content)
	assert.Equal(t, 1, searchHits)

	require.NoError(t, c.Add(context.Background(), "k8s deploy notes"))
	assert.Equal(t, 1, addHits)
}

// readAllBody is a tiny helper to keep the test file standalone.
func readAllBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}
