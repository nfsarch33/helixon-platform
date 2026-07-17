package memory

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

// EngramBackend wraps the HTTP EngramClient with a fail-open
// InMemoryBackend. When the central Engram server is unreachable
// (network error, 5xx, timeout), every operation transparently
// delegates to the fallback. This is the v17802 DRL rule
// drift-9.x-engram-backend-fail-open: the agent runtime must
// never block critical-path work on a memory write.
//
// When the Engram server is reachable, this backend writes
// to BOTH the Engram server and the fallback. The fallback
// thus acts as a local hot cache that survives Engram restarts.
type EngramBackend struct {
	client   *EngramClient
	fallback Backend
	logger   *slog.Logger

	storeCount    atomic.Int64
	recallCount   atomic.Int64
	searchCount   atomic.Int64
	fallbackCount atomic.Int64
	lastStoreUnix atomic.Int64
}

// Compile-time check.
var _ Backend = (*EngramBackend)(nil)

// NewEngramBackend wires an HTTP Engram client with an in-memory
// fallback. The fallback MUST be non-nil.
func NewEngramBackend(cfg EngramConfig, fallback Backend) *EngramBackend {
	if fallback == nil {
		fallback = NewInMemoryBackend()
	}
	return &EngramBackend{
		client:   NewEngramClient(cfg, nil),
		fallback: fallback,
		logger:   slog.Default().With(slog.String("component", "helixon.memory.engram_backend")),
	}
}

// Store writes to Engram first, falling back to the in-memory
// backend on transient errors.
func (b *EngramBackend) Store(ctx context.Context, entry *Memory) error {
	if entry == nil {
		return errors.New("memory: nil entry")
	}
	if entry.ID == "" {
		entry.ID = newID()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}

	// Always write to the fallback first (local hot cache).
	if err := b.fallback.Store(ctx, entry); err != nil {
		return err
	}

	// Best-effort write to Engram.
	_, err := b.client.Add(ctx, entry.Content, entry.AppID, entry.UserID, entry.TenantID)
	if err != nil {
		b.fallbackCount.Add(1)
		b.logger.Warn("engram store failed; using fallback", slog.String("err", err.Error()))
		// Do not propagate error: fail-open per DRL rule.
	}

	b.storeCount.Add(1)
	b.lastStoreUnix.Store(time.Now().Unix())
	return nil
}

// Recall tries the Engram server first; on miss or transient error,
// consults the local fallback.
func (b *EngramBackend) Recall(ctx context.Context, id, tenantID string) (*Memory, error) {
	// Always check fallback first to keep the read path simple and
	// avoid a network round-trip when the entry is local-recent.
	if e, err := b.fallback.Recall(ctx, id, tenantID); err == nil {
		b.recallCount.Add(1)
		return e, nil
	}

	got, err := b.client.Get(ctx, id)
	if err != nil {
		b.fallbackCount.Add(1)
		if errors.Is(err, ErrMemoryNotFound) {
			return nil, ErrMemoryNotFound
		}
		return nil, ErrMemoryNotFound // fail-closed on Recall: the fallback already had the same answer
	}
	b.recallCount.Add(1)
	return got, nil
}

// Search delegates to the local fallback. The Engram server is
// the long-term source of truth but for v17802 (MVP-5) we keep
// the read path on the hot cache to avoid network round-trips
// on every agent step.
func (b *EngramBackend) Search(ctx context.Context, query, appID, userID, tenantID string, limit int) ([]SearchResult, error) {
	results, err := b.fallback.Search(ctx, query, appID, userID, tenantID, limit)
	if err != nil {
		return nil, err
	}
	b.searchCount.Add(1)
	return results, nil
}

// Flush flushes the fallback. The Engram client writes inline so
// there is nothing to flush upstream.
func (b *EngramBackend) Flush(ctx context.Context) error {
	return b.fallback.Flush(ctx)
}

// Close releases the fallback. The HTTP client has no resources
// to release beyond what net/http manages via GC.
func (b *EngramBackend) Close() error {
	return b.fallback.Close()
}

// Stats returns a merged view.
func (b *EngramBackend) Stats() Stats {
	fb := b.fallback.Stats()
	return Stats{
		Backend:       "engram+failsafe",
		Count:         fb.Count,
		StoreCount:    b.storeCount.Load(),
		RecallCount:   b.recallCount.Load(),
		SearchCount:   b.searchCount.Load(),
		FallbackCount: b.fallbackCount.Load(),
		LastStoreUnix: b.lastStoreUnix.Load(),
	}
}
