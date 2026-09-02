package memory

// Cross-component integration test: exercises the hybrid searcher end-to-end
// against a real (sqlite) database and an httptest-backed Engram stub.
//
// This validates:
//   - Schema migration via EnsureSchema
//   - Canonical write path: HybridSearcher.Write → Engram (httptest) + local FTS5 mirror
//   - Federated Search merges FTS5 results
//   - Canonical/secondary asymmetry: Engram failure does not lose the FTS index
//   - Read path returns canonical Memory from Engram

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engramStub is a thread-safe httptest-backed Engram server speaking the
// canonical daemon dialect (messages as strings, text/workspace_id fields,
// array add response). Each method (POST /memories, GET /memories/{id},
// POST /search) is recorded so the integration test can assert on the
// request shape; a client regression to the old mem0-style dialect fails
// decoding here with 400, mirroring the live daemon.
type engramStub struct {
	mu        sync.Mutex
	memories  map[string]Memory
	requests  []stubRequest
	failNext  bool // when true, the next /memories call returns 500
	failCount int  // number of consecutive failures (decremented each call)
}

type stubRequest struct {
	method string
	path   string
	body   string
}

func newEngramStub() *engramStub {
	return &engramStub{memories: map[string]Memory{}}
}

func (s *engramStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		body := ""
		if r.Body != nil {
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			body = string(buf[:n])
		}
		s.requests = append(s.requests, stubRequest{r.Method, r.URL.Path, body})

		if s.failNext && (s.failCount == 0 || s.failCount > 0) {
			if s.failCount > 0 {
				s.failCount--
			} else {
				s.failNext = false
			}
			http.Error(w, "engram stub: induced failure", http.StatusInternalServerError)
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/memories":
			var req struct {
				Messages    []string `json:"messages"`
				AppID       string   `json:"app_id"`
				UserID      string   `json:"user_id"`
				WorkspaceID string   `json:"workspace_id"`
			}
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
				return
			}
			if len(req.Messages) == 0 {
				http.Error(w, `{"error":"messages must not be empty"}`, http.StatusBadRequest)
				return
			}
			id := "mem-stub-" + time.Now().Format("150405.000000")
			m := Memory{
				ID:        id,
				Content:   req.Messages[0],
				AppID:     req.AppID,
				UserID:    req.UserID,
				TenantID:  req.WorkspaceID,
				CreatedAt: time.Now().UTC(),
			}
			s.memories[id] = m
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode([]map[string]any{stubWireRecord(m)})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/memories/"):
			id := strings.TrimPrefix(r.URL.Path, "/memories/")
			m, ok := s.memories[id]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(stubWireRecord(m))
		case r.Method == http.MethodPost && r.URL.Path == "/search":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.Error(w, "not implemented in stub", http.StatusNotImplemented)
		}
	})
}

// stubWireRecord encodes a Memory in the canonical daemon record shape
// (text / workspace_id), matching engram's httpapi MemoryRecord JSON.
func stubWireRecord(m Memory) map[string]any {
	return map[string]any{
		"id":           m.ID,
		"text":         m.Content,
		"app_id":       m.AppID,
		"user_id":      m.UserID,
		"workspace_id": m.TenantID,
		"created_at":   m.CreatedAt,
	}
}

// openTempSQLite opens a throwaway in-memory mirror through OpenLocalIndex.
// These tests are about hybrid-search semantics, not the disk: with a file
// under t.TempDir() every SQLite statement rode the host's write path, and
// under the full-suite race run that alone overran the tests' contexts
// (EnsureSchema past 10s, an end-to-end write past 30s). The file-backed
// opener is pinned separately in localindex_test.go.
func openTempSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenLocalIndex(context.Background(), MemoryIndexDSN)
	require.NoError(t, err, "open in-memory local index")
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestHybridSearcher_EndToEndWriteSearch: write 3 memories, federated
// Search returns at least one FTS5 hit. This validates the canonical
// (Engram) + secondary (FTS5) dual-write path is operational.
func TestHybridSearcher_EndToEndWriteSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := newEngramStub()
	srv := httptest.NewServer(stub.handler())
	defer func() { srv.Close() }()

	db := openTempSQLite(t)
	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL}, nil)
	h := NewHybridSearcher(db, engram, HybridSearchConfig{}, nil)
	require.NoError(t, h.EnsureSchema(ctx))

	// Write 3 memories via canonical path.
	for _, content := range []string{
		"helixon platform integration test memory",
		"another memory about agent evaluation",
		"third memory for FTS5 ranking",
	} {
		_, err := h.Write(ctx, content, "helixon-test", "user-test", "")
		require.NoError(t, err)
	}

	// Federated Search should hit FTS5 (memories were indexed locally even
	// though Engram stub returns nothing on search).
	results, err := h.Search(ctx, "helixon integration", "helixon-test", "user-test", "")
	require.NoError(t, err)
	require.NotEmpty(t, results, "expected at least one FTS5 hit")
	for _, r := range results {
		assert.NotEmpty(t, r.ID)
		assert.NotEmpty(t, r.Content)
		assert.NotZero(t, r.Score)
	}

	// Read canonical from Engram.
	stub.mu.Lock()
	firstID := ""
	for id := range stub.memories {
		firstID = id
		break
	}
	stub.mu.Unlock()
	require.NotEmpty(t, firstID, "Engram stub should have recorded at least one memory")

	got, err := h.Read(ctx, firstID)
	require.NoError(t, err)
	assert.Equal(t, firstID, got.ID)
}

// TestHybridSearcher_CanonicalAsymmetry: when Engram returns 500
// repeatedly (more than MaxRetries), the canonical Write fails but
// the secondary (FTS5) mirror is non-fatal when it succeeds. This
// documents ADR feedback-dual-write-canonical-asymmetry.
func TestHybridSearcher_CanonicalAsymmetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stub := newEngramStub()
	srv := httptest.NewServer(stub.handler())
	defer func() { srv.Close() }()

	db := openTempSQLite(t)
	// Configure retries=0 so a single 500 fails the canonical write.
	engram := NewEngramClient(EngramConfig{BaseURL: srv.URL, MaxRetries: 0}, nil)
	h := NewHybridSearcher(db, engram, HybridSearchConfig{}, nil)
	require.NoError(t, h.EnsureSchema(ctx))

	// Induce Engram failure on the next 4 calls (more than any sane retry).
	stub.mu.Lock()
	stub.failNext = true
	stub.failCount = 4
	stub.mu.Unlock()

	_, err := h.Write(ctx, "this should fail at the canonical layer", "app", "user", "")
	require.Error(t, err, "canonical Write must fail when Engram returns 500")
	assert.Contains(t, err.Error(), "engram")

	// Recovery: next Write succeeds (failNext/failCount are consumed).
	_, err = h.Write(ctx, "this should succeed and reach FTS5", "app", "user", "")
	require.NoError(t, err, "second Write should succeed after single-shot failure")

	results, err := h.Search(ctx, "succeed FTS5", "app", "user", "")
	require.NoError(t, err)
	assert.NotEmpty(t, results, "FTS5 mirror should hold the successful Write")
}

// TestHybridSearcher_MissingEngram: when Engram client is nil, all
// canonical operations fail with a clear configuration error. This is
// the same invariant that builtins.MemoryTool relies on.
func TestHybridSearcher_MissingEngram(t *testing.T) {
	ctx := context.Background()
	db := openTempSQLite(t)
	h := NewHybridSearcher(db, nil, HybridSearchConfig{}, nil)

	_, err := h.Write(ctx, "x", "app", "user", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "engram client not configured")

	_, err = h.Read(ctx, "abc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "engram client not configured")
}
