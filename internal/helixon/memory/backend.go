// Package memory backend interface and implementations.
//
// The Backend abstraction lets the agent runtime swap memory providers
// without changing call sites. Two concrete backends are provided:
//
//   - InMemoryBackend: process-local, no network, used as the fail-open
//     fallback inside EngramBackend and as the default in tests.
//   - EngramBackend: HTTP client to the central Engram memory service,
//     with a transparent InMemoryBackend fallback when the Engram server
//     is unreachable. The fail-open behaviour is enforced by the
//     v17802 DRL rule drift-9.x-engram-backend-fail-open.
//
// Wiring rule: callers MUST treat Store/Recall/Search/Flush as
// best-effort and never block critical-path agent work on a memory
// write. Use FlushPolicy (in internal/helixon/agent/checkpoint) to
// batch and bound the call rate.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Backend is the contract every memory backend must satisfy.
//
// Methods return errors only for programming bugs; transient infra
// failures (network, 5xx) MUST be handled inside the implementation
// (e.g. via the fail-open fallback in EngramBackend). Callers should
// treat a returned error as a stop-the-line condition.
type Backend interface {
	// Store persists a memory entry. Implementations must:
	//   - assign a non-empty ID and CreatedAt if absent
	//   - be safe for concurrent callers
	//   - return nil on success even if the write was deferred
	Store(ctx context.Context, entry *Memory) error
	// Recall fetches a memory entry by ID. tenantID enforces tenant
	// isolation: an empty tenantID matches any entry (legacy behavior);
	// a non-empty tenantID returns ErrMemoryNotFound for entries owned
	// by a different tenant or by an empty-TenantID legacy entry.
	// Per v18684-4 multi-tenancy hardening.
	Recall(ctx context.Context, id, tenantID string) (*Memory, error)
	// Search returns memories whose Content contains the query
	// substring (case-insensitive). limit caps the result count; <=0
	// means default of 10. tenantID filters by tenant — empty string
	// matches all tenants (backward-compat for legacy entries); a
	// non-empty tenantID restricts results to that tenant and legacy
	// (empty-TenantID) entries, so caller sees its own data + the
	// pre-migration global pool. Per v18684-4 multi-tenancy hardening.
	Search(ctx context.Context, query, appID, userID, tenantID string, limit int) ([]SearchResult, error)
	// Flush forces any buffered writes to durable storage. In-memory
	// backends treat this as a no-op; HTTP-backed backends should
	// flush any pending batch.
	Flush(ctx context.Context) error
	// Close releases any resources held by the backend.
	Close() error
	// Stats returns operational counters for observability.
	Stats() Stats
}

// Stats is the operational view of a Backend.
type Stats struct {
	Backend       string // "in-memory" | "engram" | "engram+failsafe"
	Count         int64
	StoreCount    int64
	RecallCount   int64
	SearchCount   int64
	FallbackCount int64 // EngramBackend: incremented when fallback path triggered
	LastStoreUnix int64
}

// ID returns a new opaque memory ID. Exposed for callers that need
// to pre-allocate IDs (e.g. checkpoint maps).
func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// InMemoryBackend is the process-local fallback and test default.
type InMemoryBackend struct {
	mu          sync.RWMutex
	entries     map[string]*Memory
	storeCount  atomic.Int64
	recallCount atomic.Int64
	searchCount atomic.Int64
}

// Compile-time check.
var _ Backend = (*InMemoryBackend)(nil)

// NewInMemoryBackend returns an empty in-memory backend.
func NewInMemoryBackend() *InMemoryBackend {
	return &InMemoryBackend{entries: make(map[string]*Memory)}
}

func (b *InMemoryBackend) Store(_ context.Context, entry *Memory) error {
	if entry == nil {
		return fmt.Errorf("memory: nil entry")
	}
	if entry.ID == "" {
		entry.ID = newID()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	b.mu.Lock()
	b.entries[entry.ID] = entry
	b.mu.Unlock()
	b.storeCount.Add(1)
	return nil
}

func (b *InMemoryBackend) Recall(_ context.Context, id, tenantID string) (*Memory, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	e, ok := b.entries[id]
	if !ok {
		return nil, ErrMemoryNotFound
	}
	// Tenant isolation: if a tenantID is requested, the entry must either
	// have no tenant (legacy) or match the requested tenant. Per v18684-4.
	if tenantID != "" && e.TenantID != "" && e.TenantID != tenantID {
		return nil, ErrMemoryNotFound
	}
	b.recallCount.Add(1)
	return e, nil
}

func (b *InMemoryBackend) Search(_ context.Context, query, appID, userID, tenantID string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	q := strings.ToLower(query)
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]SearchResult, 0, len(b.entries))
	for _, e := range b.entries {
		if appID != "" && e.AppID != appID {
			continue
		}
		if userID != "" && e.UserID != userID {
			continue
		}
		// Tenant isolation: an entry is visible to tenantID if either
		// the entry has no tenant (legacy / pre-migration) or its
		// TenantID matches the caller. An empty tenantID matches
		// everything (backward-compat for callers that have not been
		// tenant-stamped yet).
		if tenantID != "" && e.TenantID != "" && e.TenantID != tenantID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Content), q) {
			continue
		}
		out = append(out, SearchResult{Memory: *e, Score: 1.0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	b.searchCount.Add(1)
	return out, nil
}

func (b *InMemoryBackend) Flush(_ context.Context) error { return nil }
func (b *InMemoryBackend) Close() error                  { return nil }

func (b *InMemoryBackend) Stats() Stats {
	b.mu.RLock()
	count := int64(len(b.entries))
	b.mu.RUnlock()
	return Stats{
		Backend:       "in-memory",
		Count:         count,
		StoreCount:    b.storeCount.Load(),
		RecallCount:   b.recallCount.Load(),
		SearchCount:   b.searchCount.Load(),
		LastStoreUnix: time.Now().Unix(),
	}
}
