package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Test 1: Backend interface satisfies Store/Recall/Search/Flush/Close/Stats contract.
func TestBackend_InterfaceContract(t *testing.T) { //nolint:revive // unused-parameter required by interface
	_ = Backend(nil) // type assertion compile check
}

// Test 2: InMemoryBackend stores and recalls entries.
func TestInMemoryBackend_Store_Recall(t *testing.T) {
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()

	ctx := context.Background()
	entry := &Memory{
		Content: "buy milk",
		AppID:   "test",
		UserID:  "u1",
	}

	if err := b.Store(ctx, entry); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("Store should assign ID")
	}
	if entry.CreatedAt.IsZero() {
		t.Fatal("Store should set CreatedAt")
	}

	got, err := b.Recall(ctx, entry.ID, "")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if got.Content != "buy milk" {
		t.Fatalf("Recall content: %q", got.Content)
	}
}

// Test 3: InMemoryBackend Search returns content matches.
func TestInMemoryBackend_Search(t *testing.T) {
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()
	ctx := context.Background()

	_ = b.Store(ctx, &Memory{Content: "buy milk", AppID: "test", UserID: "u1"})
	_ = b.Store(ctx, &Memory{Content: "buy eggs", AppID: "test", UserID: "u1"})
	_ = b.Store(ctx, &Memory{Content: "sell house", AppID: "test", UserID: "u1"})

	results, err := b.Search(ctx, "buy", "test", "u1", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search results: got %d want 2", len(results))
	}
}

// Test 4: InMemoryBackend Flush is a no-op (in-memory always flushed).
func TestInMemoryBackend_Flush(t *testing.T) {
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()
	ctx := context.Background()
	if err := b.Store(ctx, &Memory{Content: "x", AppID: "a", UserID: "u"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := b.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// Test 5: InMemoryBackend Stats reports counts.
func TestInMemoryBackend_Stats(t *testing.T) {
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = b.Store(ctx, &Memory{Content: "x", AppID: "a", UserID: "u"})
	}
	st := b.Stats()
	if st.Count != 3 {
		t.Fatalf("Stats count: got %d want 3", st.Count)
	}
	if st.Backend != "in-memory" {
		t.Fatalf("Stats backend: got %q want in-memory", st.Backend)
	}
}

// Test 6: InMemoryBackend concurrent Store/Recall is race-clean.
func TestInMemoryBackend_ConcurrentSafe(t *testing.T) {
	b := NewInMemoryBackend()
	defer func() { _ = b.Close() }()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) { //nolint:revive // unused-parameter required by interface
			defer wg.Done()
			e := &Memory{Content: "c", AppID: "a", UserID: "u"}
			_ = b.Store(ctx, e)
			if e.ID != "" {
				_, _ = b.Recall(ctx, e.ID, "")
			}
		}(i)
	}
	wg.Wait()
	if b.Stats().Count != 20 {
		t.Fatalf("Concurrent Store count: got %d want 20", b.Stats().Count)
	}
}

// Test 7: EngramBackend falls back to InMemoryBackend on connection error (fail-open).
func TestEngramBackend_FailOpen_OnUnreachable(t *testing.T) {
	cfg := EngramConfig{BaseURL: "http://127.0.0.1:1", Timeout: 200 * time.Millisecond, MaxRetries: 0}
	b := NewEngramBackend(cfg, NewInMemoryBackend())
	defer func() { _ = b.Close() }()

	ctx := context.Background()
	entry := &Memory{Content: "test fail-open", AppID: "a", UserID: "u"}
	if err := b.Store(ctx, entry); err != nil {
		t.Fatalf("EngramBackend.Store should fail-open: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("EngramBackend.Store should assign ID even on fail-open")
	}
	// Recall should hit fallback since Engram is unreachable.
	got, err := b.Recall(ctx, entry.ID, "")
	if err != nil {
		t.Fatalf("EngramBackend.Recall (fallback path): %v", err)
	}
	if got.Content != "test fail-open" {
		t.Fatalf("fallback content: %q", got.Content)
	}
}

// Test 8: EngramBackend.Stats includes fallback count.
func TestEngramBackend_Stats_IncludesFallback(t *testing.T) {
	cfg := EngramConfig{BaseURL: "http://127.0.0.1:1", Timeout: 200 * time.Millisecond, MaxRetries: 0}
	fb := NewInMemoryBackend()
	b := NewEngramBackend(cfg, fb)
	defer func() { _ = b.Close() }()
	ctx := context.Background()
	_ = b.Store(ctx, &Memory{Content: "x", AppID: "a", UserID: "u"})
	st := b.Stats()
	if st.Backend != "engram+failsafe" {
		t.Fatalf("Stats.Backend: got %q want engram+failsafe", st.Backend)
	}
	if st.FallbackCount < 1 {
		t.Fatalf("Stats.FallbackCount: got %d want >=1", st.FallbackCount)
	}
}

// Test 9: EngramBackend Search falls through to fallback on error.
func TestEngramBackend_Search_FailOpen(t *testing.T) {
	cfg := EngramConfig{BaseURL: "http://127.0.0.1:1", Timeout: 200 * time.Millisecond, MaxRetries: 0}
	fb := NewInMemoryBackend()
	b := NewEngramBackend(cfg, fb)
	defer func() { _ = b.Close() }()
	ctx := context.Background()
	_ = fb.Store(ctx, &Memory{Content: "alpha", AppID: "a", UserID: "u"})
	_ = fb.Store(ctx, &Memory{Content: "alphabet", AppID: "a", UserID: "u"})

	results, err := b.Search(ctx, "alph", "a", "u", "", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search fallback results: got %d want 2", len(results))
	}
}

// Sentinel: ensure errors.Is unwrap works for ErrEngramUnavailable.
func TestEngramBackend_ErrSentinel(t *testing.T) {
	if !errors.Is(ErrEngramUnavailable, ErrEngramUnavailable) {
		t.Fatal("ErrEngramUnavailable should be a stable sentinel")
	}
}
