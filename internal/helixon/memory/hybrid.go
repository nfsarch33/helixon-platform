package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// HybridResult combines FTS5 and vector search results with a unified score.
type HybridResult struct {
	ID       string  `json:"id"`
	Content  string  `json:"content"`
	TenantID string  `json:"tenant_id,omitempty"`
	Source   string  `json:"source"`
	Score    float64 `json:"score"`
}

// HybridSearchConfig controls the blending of FTS5, vector, and Mem0 search.
type HybridSearchConfig struct {
	FTSWeight    float64
	VectorWeight float64
	Mem0Weight   float64
	MaxResults   int
}

func (c HybridSearchConfig) withDefaults() HybridSearchConfig {
	if c.FTSWeight <= 0 {
		c.FTSWeight = 0.3
	}
	if c.VectorWeight <= 0 {
		c.VectorWeight = 0.5
	}
	if c.Mem0Weight <= 0 {
		c.Mem0Weight = 0.2
	}
	if c.MaxResults <= 0 {
		c.MaxResults = 20
	}
	return c
}

// HybridSearcher federates SQLite FTS5 (local history), Engram vector
// search, and Mem0 (cross-session associative memory) into a single
// ranked result list. Mem0 is optional; if not wired the searcher
// falls back to FTS5 + Engram only.
type HybridSearcher struct {
	db     *sql.DB
	engram *EngramClient
	mem0   Mem0Client
	cfg    HybridSearchConfig
	logger *slog.Logger
	mu     sync.RWMutex
}

// NewHybridSearcher creates a searcher that blends local FTS5 with Engram vector search.
func NewHybridSearcher(db *sql.DB, engram *EngramClient, cfg HybridSearchConfig, logger *slog.Logger) *HybridSearcher {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &HybridSearcher{
		db:     db,
		engram: engram,
		cfg:    cfg,
		logger: logger.With(slog.String("component", "helixon.memory.hybrid")),
	}
}

// WithMem0 wires a Mem0Client into the searcher so federated Search and
// Write include the Mem0 backend. Passing nil clears the client.
func (h *HybridSearcher) WithMem0(client Mem0Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mem0 = client
}

// Search runs FTS5, vector, and Mem0 (when wired) in parallel, then merges
// and re-ranks by content. A failure in any single backend is logged but
// does not fail the federated call.
//
// tenantID filters results by tenant per v18684-4 multi-tenancy
// hardening. An empty tenantID matches all tenants (legacy callers).
func (h *HybridSearcher) Search(ctx context.Context, query, appID, userID, tenantID string) ([]HybridResult, error) {
	type resultSet struct {
		results []HybridResult
		err     error
	}

	h.mu.RLock()
	mem0 := h.mem0
	h.mu.RUnlock()

	ftsCh := make(chan resultSet, 1)
	vecCh := make(chan resultSet, 1)
	memCh := make(chan resultSet, 1)

	go func() {
		results, err := h.ftsSearch(ctx, query)
		ftsCh <- resultSet{results, err}
	}()

	go func() {
		results, err := h.vectorSearch(ctx, query, appID, userID, tenantID)
		vecCh <- resultSet{results, err}
	}()

	go func() {
		if mem0 == nil {
			memCh <- resultSet{}
			return
		}
		results, err := h.mem0Search(ctx, mem0, query)
		memCh <- resultSet{results, err}
	}()

	var ftsResults, vecResults, memResults []HybridResult

	ftsRes := <-ftsCh
	if ftsRes.err != nil {
		h.logger.Warn("FTS5 search failed, proceeding with other backends", slog.String("error", ftsRes.err.Error()))
	} else {
		ftsResults = ftsRes.results
	}

	vecRes := <-vecCh
	if vecRes.err != nil {
		h.logger.Warn("vector search failed, proceeding with other backends", slog.String("error", vecRes.err.Error()))
	} else {
		vecResults = vecRes.results
	}

	memRes := <-memCh
	if memRes.err != nil {
		h.logger.Warn("mem0 search failed, proceeding with other backends", slog.String("error", memRes.err.Error()))
	} else {
		memResults = memRes.results
	}

	merged := h.merge(ftsResults, vecResults, memResults)

	// Tenant isolation: drop entries whose TenantID does not match
	// the caller. An empty TenantID (legacy entry) is always visible.
	if tenantID != "" {
		filtered := merged[:0]
		for _, m := range merged {
			if m.TenantID == "" || m.TenantID == tenantID {
				filtered = append(filtered, m)
			}
		}
		merged = filtered
	}

	if len(merged) > h.cfg.MaxResults {
		merged = merged[:h.cfg.MaxResults]
	}
	return merged, nil
}

// Write persists a memory to the canonical Engram store and mirrors the
// content into the local FTS5 index. The canonical write is mandatory; if
// Engram fails, no local index entry is written and the error propagates.
// If the local FTS5 mirror fails after a green canonical write, the error
// is logged but the canonical Memory is still returned (canonical/secondary
// asymmetry per ADR feedback-dual-write-canonical-asymmetry).
//
// tenantID stamps the entry with the tenant scope per v18684-4
// multi-tenancy hardening; pass "" for legacy callers.
func (h *HybridSearcher) Write(ctx context.Context, content, appID, userID, tenantID string) (*Memory, error) {
	if h.engram == nil {
		return nil, fmt.Errorf("hybrid: engram client not configured")
	}
	mem, err := h.engram.Add(ctx, content, appID, userID, tenantID)
	if mem != nil && mem.TenantID == "" {
		mem.TenantID = tenantID
	}
	if err != nil {
		return nil, fmt.Errorf("hybrid: engram write: %w", err)
	}
	if mem == nil {
		return nil, fmt.Errorf("hybrid: engram returned nil memory")
	}
	if h.db != nil {
		if err := h.IndexLocal(ctx, mem.ID, mem.Content); err != nil {
			h.logger.Warn("local FTS mirror failed (non-fatal)",
				slog.String("id", mem.ID), slog.String("error", err.Error()))
		}
	}
	h.mu.RLock()
	mem0 := h.mem0
	h.mu.RUnlock()
	if mem0 != nil {
		if err := mem0.Add(ctx, mem.Content); err != nil {
			h.logger.Warn("mem0 mirror failed (non-fatal)",
				slog.String("id", mem.ID), slog.String("error", err.Error()))
		}
	}
	return mem, nil
}

// Read fetches a single memory by id from the canonical Engram store.
func (h *HybridSearcher) Read(ctx context.Context, id string) (*Memory, error) {
	if h.engram == nil {
		return nil, fmt.Errorf("hybrid: engram client not configured")
	}
	if id == "" {
		return nil, fmt.Errorf("hybrid: memory id is required")
	}
	return h.engram.Get(ctx, id)
}

// IndexLocal inserts a document into the local FTS5 index for full-text search.
func (h *HybridSearcher) IndexLocal(ctx context.Context, id, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := h.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO local_memories (id, content, indexed_at) VALUES (?, ?, ?)`,
		id, content, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// EnsureSchema creates the local FTS5 tables if they don't exist.
func (h *HybridSearcher) EnsureSchema(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS local_memories (
	id         TEXT PRIMARY KEY,
	content    TEXT NOT NULL,
	indexed_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS local_memories_fts USING fts5(
	content,
	id UNINDEXED,
	content=local_memories,
	content_rowid=rowid
);

CREATE TRIGGER IF NOT EXISTS local_memories_ai AFTER INSERT ON local_memories BEGIN
	INSERT INTO local_memories_fts(rowid, content, id) VALUES (new.rowid, new.content, new.id);
END;

CREATE TRIGGER IF NOT EXISTS local_memories_ad AFTER DELETE ON local_memories BEGIN
	INSERT INTO local_memories_fts(local_memories_fts, rowid, content, id)
	VALUES ('delete', old.rowid, old.content, old.id);
END;
`
	_, err := h.db.ExecContext(ctx, ddl)
	return err
}

func (h *HybridSearcher) ftsSearch(ctx context.Context, query string) ([]HybridResult, error) {
	if h.db == nil {
		return nil, nil
	}

	rows, err := h.db.QueryContext(ctx,
		`SELECT lm.id, lm.content, rank
		 FROM local_memories_fts f
		 JOIN local_memories lm ON lm.rowid = f.rowid
		 WHERE local_memories_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`, query, h.cfg.MaxResults)
	if err != nil {
		return nil, fmt.Errorf("FTS5 query: %w", err)
	}
	defer rows.Close()

	var results []HybridResult
	for rows.Next() {
		var r HybridResult
		var rank float64
		if err := rows.Scan(&r.ID, &r.Content, &rank); err != nil {
			return nil, fmt.Errorf("scan FTS result: %w", err)
		}
		r.Source = "fts5"
		r.Score = -rank
		results = r.normalize(results)
	}
	return results, rows.Err()
}

func (r HybridResult) normalize(results []HybridResult) []HybridResult {
	return append(results, r)
}

func (h *HybridSearcher) vectorSearch(ctx context.Context, query, appID, userID, tenantID string) ([]HybridResult, error) {
	if h.engram == nil {
		return nil, nil
	}

	searchResults, err := h.engram.Search(ctx, query, appID, userID, tenantID, h.cfg.MaxResults)
	if err != nil {
		return nil, err
	}

	results := make([]HybridResult, 0, len(searchResults))
	for _, sr := range searchResults {
		results = append(results, HybridResult{
			ID:       sr.ID,
			Content:  sr.Content,
			TenantID: sr.TenantID,
			Source:   "engram",
			Score:    sr.Score,
		})
	}
	return results, nil
}

func (h *HybridSearcher) mem0Search(ctx context.Context, client Mem0Client, query string) ([]HybridResult, error) {
	raw, err := client.Search(ctx, query, h.cfg.MaxResults)
	if err != nil {
		return nil, err
	}
	out := make([]HybridResult, 0, len(raw))
	for _, r := range raw {
		out = append(out, HybridResult{
			ID:      r.ID,
			Content: r.Content,
			Source:  "mem0",
			Score:   r.Score,
		})
	}
	return out, nil
}

func (h *HybridSearcher) merge(fts, vector, mem0 []HybridResult) []HybridResult {
	type contrib struct {
		entry  HybridResult
		score  float64
		voters []string
	}
	seen := make(map[string]*contrib)

	add := func(rs []HybridResult, weight float64) {
		mx := maxScore(rs)
		if mx == 0 {
			mx = 1
		}
		for i := range rs {
			normalized := (rs[i].Score / mx) * weight
			key := rs[i].Content
			if existing, ok := seen[key]; ok {
				existing.score += normalized
				existing.voters = append(existing.voters, rs[i].Source)
			} else {
				seen[key] = &contrib{
					entry:  rs[i],
					score:  normalized,
					voters: []string{rs[i].Source},
				}
			}
		}
	}

	add(fts, h.cfg.FTSWeight)
	add(vector, h.cfg.VectorWeight)
	add(mem0, h.cfg.Mem0Weight)

	results := make([]HybridResult, 0, len(seen))
	for _, c := range seen {
		entry := c.entry
		entry.Score = c.score
		if len(c.voters) > 1 {
			entry.Source = "hybrid"
		}
		results = append(results, entry)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

func maxScore(results []HybridResult) float64 {
	m := 0.0
	for _, r := range results {
		if r.Score > m {
			m = r.Score
		}
	}
	return m
}
