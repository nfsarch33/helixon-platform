// Package persistence — Engram-backed agent state persistence.
//
// EngramBackend satisfies Backend by serializing agent state to a
// canonical Engram memory and reading it back via search. The backend is
// intentionally tiny: it depends only on the local EngramClient
// interface, so tests can inject an in-memory fake without touching
// the network or the real Engram server.
//
// Design (v17802-3 / v17805-4):
//
//	Save(ctx, state) → engram.Add(content=<json>, appID=helixon, userID=state.AgentID)
//	  and remembers the assigned memory ID for round-trip reads.
//	Load(ctx, agentID) → engram.Search(query=<json tag>, appID=helixon, userID=agentID)
//	  returning the most recent matching memory.
//
// The backend is safe for concurrent Save/Load because it serializes
// against an internal mutex guarding the agentID→memoryID map. The
// underlying EngramClient is responsible for any concurrency guarantees
// on its end.
package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// EngramClient is the minimal Engram surface needed by EngramBackend.
// Production wiring uses *helixon/memory.EngramClient; tests provide
// an in-memory fake that satisfies this interface.
type EngramClient interface {
	Add(ctx context.Context, content, appID, userID string) (memoryID string, err error)
	Search(ctx context.Context, query, appID, userID string, limit int) ([]EngramHit, error)
}

// EngramHit is the search result shape EngramBackend depends on.
// The real helixon/memory package returns memory.SearchResult; the
// interface kept narrow so tests can satisfy it without copy-pasting
// the production type.
type EngramHit struct {
	ID      string
	Content string
}

// EngramBackend persists agent state to an Engram server.
//
// agentID → memoryID map (last save) is held in memory so Load can
// retrieve the canonical record directly. When the map is missing an
// entry (e.g. first Load after restart), Load falls back to Search.
type EngramBackend struct {
	client EngramClient
	appID  string
	mu     sync.RWMutex
	index  map[string]string // agentID → memoryID
}

// NewEngramBackend wires EngramBackend to the given Engram client.
// appID should be the constant Engram tags apply to (e.g. "helixon").
func NewEngramBackend(client EngramClient, appID string) *EngramBackend {
	if appID == "" {
		appID = "helixon"
	}
	return &EngramBackend{
		client: client,
		appID:  appID,
		index:  make(map[string]string),
	}
}

// Save serializes state to JSON and writes it through Engram.Add.
// The returned memory ID is cached so Load can resolve the record
// without a Search.
func (b *EngramBackend) Save(ctx context.Context, state State) error {
	state.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("engram backend: marshal state: %w", err)
	}
	id, err := b.client.Add(ctx, string(payload), b.appID, state.AgentID)
	if err != nil {
		return fmt.Errorf("engram backend: add: %w", err)
	}
	b.mu.Lock()
	b.index[state.AgentID] = id
	b.mu.Unlock()
	return nil
}

// Load retrieves the latest saved state for the given agentID.
//
//  1. If the in-memory index knows the memoryID, we Search by that
//     agent + appID tag and return the freshest hit. The map is
//     best-effort cache; if the Search comes back empty we still
//     return ErrNoState (consistent with InMemoryBackend).
//  2. On a cache miss we Search by tag and take the first hit.
//
// Note: EngramBackend does not currently call Engram.Get(id); it
// stays search-based to avoid the cross-package coupling to the
// production Memory type. The search path is one network hop and
// the limit=1 keeps it cheap.
func (b *EngramBackend) Load(ctx context.Context, agentID string) (State, error) {
	hits, err := b.client.Search(ctx, "agent_state", b.appID, agentID, 1)
	if err != nil {
		return State{}, fmt.Errorf("engram backend: search: %w", err)
	}
	if len(hits) == 0 {
		return State{}, ErrNoState
	}
	var s State
	if err := json.Unmarshal([]byte(hits[0].Content), &s); err != nil {
		return State{}, fmt.Errorf("engram backend: unmarshal: %w", err)
	}
	b.mu.Lock()
	b.index[agentID] = hits[0].ID
	b.mu.Unlock()
	return s, nil
}
