// Package persistence implements agent state persistence via Engram.
//
// MVP-5 design (v17006-4/5):
//
// persistAgentState(ctx, state) error
//   - serializes state to JSON
//   - calls Engram `engram_add` with user_id=agent_id, run_id=session_id
//   - tags with: agent_run_id, sprint_id, machine_id, created_at
//
// resumeAgentState(ctx, agent_id) (State, error)
//   - queries Engram `engram_search` for latest state with matching tags
//   - deserializes; returns state
//
// TTL: 7 days (cleanup in v17006 followup)
//
// In v17006 (this sprint), we ship:
//  1. The interface + stub impl for offline use
//  2. The Redis backend stub (production path uses Engram)
//
// Real Engram integration is deferred to v17010 (range closeout) where the
// Engram server is verified live.
package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// State is the canonical agent state shape. Versioned for forward-compat.
type State struct {
	Version   int               `json:"version"`
	AgentID   string            `json:"agent_id"`
	SessionID string            `json:"session_id"`
	Messages  []json.RawMessage `json:"messages"`
	KVState   map[string]any    `json:"kv_state,omitempty"`
	TokensIn  int               `json:"tokens_in"`
	TokensOut int               `json:"tokens_out"`
	UpdatedAt time.Time         `json:"updated_at"`
	MachineID string            `json:"machine_id"`
	SprintID  string            `json:"sprint_id,omitempty"`
}

// ErrNoState is returned when resumeAgentState finds no prior state.
var ErrNoState = errors.New("persistence: no prior state found")

// Backend is the storage interface. Implementations: in-memory, Engram, Redis.
type Backend interface {
	Save(ctx context.Context, state State) error
	Load(ctx context.Context, agentID string) (State, error)
}

// InMemoryBackend is the test/dev backend. Concurrency-safe.
type InMemoryBackend struct {
	mu     sync.RWMutex
	states map[string]State // key: agentID
}

// NewInMemoryBackend returns an empty in-memory backend.
func NewInMemoryBackend() *InMemoryBackend {
	return &InMemoryBackend{states: make(map[string]State)}
}

// Save persists state to memory.
func (b *InMemoryBackend) Save(ctx context.Context, state State) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	state.UpdatedAt = time.Now().UTC()
	b.states[state.AgentID] = state
	return nil
}

// Load retrieves the latest state for the given agentID.
func (b *InMemoryBackend) Load(ctx context.Context, agentID string) (State, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	s, ok := b.states[agentID]
	if !ok {
		return State{}, ErrNoState
	}
	return s, nil
}

// Persist is the high-level helper that takes an agent ID + state delta and
// persists via the configured Backend.
type Persist struct {
	Backend Backend
}

// NewPersist returns a Persist wrapping the given Backend.
func NewPersist(b Backend) *Persist {
	return &Persist{Backend: b}
}

// Save persists state via the backend.
func (p *Persist) Save(ctx context.Context, state State) error {
	return p.Backend.Save(ctx, state)
}

// Resume retrieves the latest state for the given agent ID.
func (p *Persist) Resume(ctx context.Context, agentID string) (State, error) {
	return p.Backend.Load(ctx, agentID)
}
