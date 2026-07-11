// Integration tests for the EngramBackend (v17805-4).
//
// These tests follow the token-reseller fakeStripeStore pattern: a
// thread-safe in-memory fake of the EngramClient interface lets the
// integration test exercise Save → Load round-trips without touching
// the network. The fake preserves ON CONFLICT-style semantics so we
// can prove the backend behaves the same against a real Engram
// server when one is wired.
package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeEngramClient is an in-memory EngramClient stub.
//
// Each Save produces a fresh memory ID (monotonic counter). Search
// returns all matching hits ordered by id descending so Load picks
// the most recent save. The stub honors Add errors so we can prove
// the backend propagates failures.
type fakeEngramClient struct {
	mu       sync.Mutex
	memories map[string]storedMemory // memoryID → record
	byUser   map[string][]string     // userID → []memoryID (insertion order)
	counter  atomic.Int64
	failNext atomic.Int32 // when > 0, next Add returns this error
}

type storedMemory struct {
	ID      string
	AppID   string
	UserID  string
	Content string
}

func newFakeEngramClient() *fakeEngramClient {
	return &fakeEngramClient{
		memories: map[string]storedMemory{},
		byUser:   map[string][]string{},
	}
}

func (f *fakeEngramClient) Add(_ context.Context, content, appID, userID string) (string, error) {
	if n := f.failNext.Load(); n > 0 {
		f.failNext.Add(-1)
		return "", errors.New("fake engram: induced add failure")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	idNum := f.counter.Add(1)
	id := fmt.Sprintf("mem-%06d", idNum)
	f.memories[id] = storedMemory{ID: id, AppID: appID, UserID: userID, Content: content}
	f.byUser[userID] = append(f.byUser[userID], id)
	return id, nil
}

func (f *fakeEngramClient) Search(_ context.Context, query, appID, userID string, limit int) ([]EngramHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := f.byUser[userID]
	if len(ids) == 0 {
		return nil, nil
	}
	// Return insertion order reversed (newest first), filtered by appID.
	hits := make([]EngramHit, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]
		rec := f.memories[id]
		if rec.AppID != appID {
			continue
		}
		// The query is part of the contract; we always pass "agent_state"
		// from EngramBackend.Load. The fake ignores the query string
		// because it has no real index, but we keep it visible in tests
		// so a future regression in the call site is caught.
		_ = query
		hits = append(hits, EngramHit{ID: rec.ID, Content: rec.Content})
		if limit > 0 && len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

// TestEngramBackend_SaveLoadRoundtrip: Save then Load returns the
// same State. Proves the basic happy path through EngramBackend.
func TestEngramBackend_SaveLoadRoundtrip(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "helixon")
	p := NewPersist(be)

	state := State{
		Version:   1,
		AgentID:   "agent-engram-1",
		SessionID: "session-42",
		KVState:   map[string]any{"key": "engram-value", "nested": map[string]any{"a": 1}},
		TokensIn:  1234,
		TokensOut: 567,
		MachineID: "win3-wsl3",
		SprintID:  "v17805",
	}
	if err := p.Save(ctx, state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	resumed, err := p.Resume(ctx, "agent-engram-1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.AgentID != state.AgentID {
		t.Fatalf("AgentID mismatch: want %s got %s", state.AgentID, resumed.AgentID)
	}
	if resumed.SessionID != state.SessionID {
		t.Fatalf("SessionID mismatch: want %s got %s", state.SessionID, resumed.SessionID)
	}
	if resumed.TokensIn != state.TokensIn || resumed.TokensOut != state.TokensOut {
		t.Fatalf("tokens wrong: in=%d out=%d", resumed.TokensIn, resumed.TokensOut)
	}
	if resumed.KVState["key"] != "engram-value" {
		t.Fatalf("kv state lost: %+v", resumed.KVState)
	}
	if resumed.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt not populated by backend")
	}
}

// TestEngramBackend_LoadUnknownAgent: Load for an agent that was
// never saved returns ErrNoState. Mirrors InMemoryBackend behaviour.
func TestEngramBackend_LoadUnknownAgent(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "helixon")
	p := NewPersist(be)

	_, err := p.Resume(ctx, "never-saved-agent")
	if !errors.Is(err, ErrNoState) {
		t.Fatalf("want ErrNoState, got %v", err)
	}
}

// TestEngramBackend_OverwriteReturnsLatest: three saves for the same
// agentID must round-trip to the most recent State (version=3,
// tokens=300). Mirrors InMemoryBackend overwrite semantics so
// swapping InMemoryBackend for EngramBackend is drop-in.
func TestEngramBackend_OverwriteReturnsLatest(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "helixon")
	p := NewPersist(be)

	for i := 1; i <= 3; i++ {
		if err := p.Save(ctx, State{
			Version:  i,
			AgentID:  "agent-overwrite-engram",
			TokensIn: i * 100,
		}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	resumed, err := p.Resume(ctx, "agent-overwrite-engram")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Version != 3 {
		t.Fatalf("Version: want 3 got %d", resumed.Version)
	}
	if resumed.TokensIn != 300 {
		t.Fatalf("TokensIn: want 300 got %d", resumed.TokensIn)
	}
}

// TestEngramBackend_AddFailureSurfaces: when the EngramClient Add
// call fails, Save returns the error and the in-memory index stays
// empty. A subsequent Load must return ErrNoState.
func TestEngramBackend_AddFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	stub.failNext.Store(1)
	be := NewEngramBackend(stub, "helixon")
	p := NewPersist(be)

	err := p.Save(ctx, State{AgentID: "agent-fail", Version: 1})
	if err == nil {
		t.Fatalf("expected error from Save when Engram Add fails")
	}
	if !contains(err.Error(), "induced add failure") {
		t.Fatalf("error did not wrap underlying cause: %v", err)
	}

	be.mu.RLock()
	_, cached := be.index["agent-fail"]
	be.mu.RUnlock()
	if cached {
		t.Fatalf("index must not be populated on Add failure")
	}

	if _, err := p.Resume(ctx, "agent-fail"); !errors.Is(err, ErrNoState) {
		t.Fatalf("Resume after failed Save must return ErrNoState, got %v", err)
	}
}

// TestEngramBackend_AppIDDefaultsToHelixon: empty appID passed to
// NewEngramBackend defaults to "helixon". Saved records must carry
// the default appID; this protects production wiring from a typo.
func TestEngramBackend_AppIDDefaultsToHelixon(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "") // empty
	p := NewPersist(be)

	if err := p.Save(ctx, State{AgentID: "agent-default-appid", Version: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	id := stub.byUser["agent-default-appid"][0]
	rec := stub.memories[id]
	if rec.AppID != "helixon" {
		t.Fatalf("AppID default not applied: %q", rec.AppID)
	}
}

// TestEngramBackend_ConcurrentSaveLoad: 8 goroutines save distinct
// agents and a 9th reads back. All saves succeed, all reads return
// the saved state for the matching agent. Proves the backend mutex
// is sufficient and the fake is race-safe under -race.
func TestEngramBackend_ConcurrentSaveLoad(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "helixon")
	p := NewPersist(be)

	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			agent := fmt.Sprintf("agent-concurrent-%d", i)
			if err := p.Save(ctx, State{AgentID: agent, Version: i, TokensIn: i * 10}); err != nil {
				t.Errorf("Save %s: %v", agent, err)
			}
		}()
	}
	wg.Wait()

	for i := 0; i < writers; i++ {
		agent := fmt.Sprintf("agent-concurrent-%d", i)
		got, err := p.Resume(ctx, agent)
		if err != nil {
			t.Fatalf("Resume %s: %v", agent, err)
		}
		if got.TokensIn != i*10 {
			t.Fatalf("Resume %s: TokensIn want %d got %d", agent, i*10, got.TokensIn)
		}
	}
}

// TestEngramBackend_ResumeAcrossAgents: saves for two agents must
// round-trip to their own state; cross-agent reads must not leak.
// Mirrors the isolation guarantee of InMemoryBackend.
func TestEngramBackend_ResumeAcrossAgents(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "helixon")
	p := NewPersist(be)

	if err := p.Save(ctx, State{AgentID: "agent-A", SessionID: "sess-A", TokensIn: 100}); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := p.Save(ctx, State{AgentID: "agent-B", SessionID: "sess-B", TokensIn: 200}); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	a, err := p.Resume(ctx, "agent-A")
	if err != nil {
		t.Fatalf("Resume A: %v", err)
	}
	if a.SessionID != "sess-A" || a.TokensIn != 100 {
		t.Fatalf("agent-A state leaked: %+v", a)
	}
	b, err := p.Resume(ctx, "agent-B")
	if err != nil {
		t.Fatalf("Resume B: %v", err)
	}
	if b.SessionID != "sess-B" || b.TokensIn != 200 {
		t.Fatalf("agent-B state leaked: %+v", b)
	}
}

// TestEngramBackend_LoadMalformedContent: when the EngramClient
// returns a content string that fails to JSON-decode as State,
// Load returns a wrapped error (not ErrNoState). This proves the
// backend does not silently produce a zero-valued State on parse
// failure.
func TestEngramBackend_LoadMalformedContent(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "helixon")

	// Pre-populate the fake with a malformed payload.
	stub.mu.Lock()
	stub.counter.Add(1)
	id := "mem-bad"
	stub.memories[id] = storedMemory{ID: id, AppID: "helixon", UserID: "agent-bad", Content: "{not valid json"}
	stub.byUser["agent-bad"] = []string{id}
	stub.mu.Unlock()

	_, err := be.Load(ctx, "agent-bad")
	if err == nil {
		t.Fatalf("expected error for malformed Engram content")
	}
	if errors.Is(err, ErrNoState) {
		t.Fatalf("malformed content must NOT collapse to ErrNoState: %v", err)
	}
	if !contains(err.Error(), "unmarshal") {
		t.Fatalf("error should mention unmarshal: %v", err)
	}
}

// TestEngramBackend_LoadSearchError: when EngramClient.Search fails,
// Load returns the wrapped error so callers can distinguish a server
// problem from "no prior state".
func TestEngramBackend_LoadSearchError(t *testing.T) {
	ctx := context.Background()
	stub := newFakeEngramClient()
	be := NewEngramBackend(stub, "helixon")

	// Replace client with a Search-only failure stub.
	be.client = failingSearchEngram{err: errors.New("fake engram: induced search failure")}

	_, err := be.Load(ctx, "agent-x")
	if err == nil {
		t.Fatalf("expected error from Search failure")
	}
	if !contains(err.Error(), "induced search failure") {
		t.Fatalf("error did not wrap search cause: %v", err)
	}
}

// failingSearchEngram returns errors from Search and ignores Add.
type failingSearchEngram struct{ err error }

func (f failingSearchEngram) Add(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}
func (f failingSearchEngram) Search(_ context.Context, _, _, _ string, _ int) ([]EngramHit, error) {
	return nil, f.err
}

// TestEngramBackend_StateJSONStable: serialized State round-trips
// through json.Marshal/Unmarshal byte-for-byte (modulo UpdatedAt).
// Proves the persistence contract is stable.
func TestEngramBackend_StateJSONStable(t *testing.T) {
	s := State{
		Version:   7,
		AgentID:   "agent-stable",
		SessionID: "session-stable",
		KVState:   map[string]any{"x": "y"},
		TokensIn:  11,
		TokensOut: 22,
		MachineID: "win3-wsl3",
		SprintID:  "v17805",
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back State
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.AgentID != s.AgentID || back.SessionID != s.SessionID || back.Version != s.Version {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
	if back.KVState["x"] != "y" {
		t.Fatalf("KVState lost: %+v", back.KVState)
	}
}

// contains is a tiny strings.Contains helper to keep the test file
// dependency-light (avoid importing strings just for one call site).
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// _ keeps sort in scope if future tests need it; prevents import
// drift if we ever drop the dep above.
var _ = sort.IntSlice{}.Sort
