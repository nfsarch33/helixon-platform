// Agentrace + metrics emit for LoopGuard. v17003-4.
//
// LoopTripEvent is appended to a NDJSON log file at ~/logs/runx/agentrace-mcp.ndjson
// (or whatever path the agent runner configures). The schema is intentionally
// compatible with the existing agentrace schema in internal/helixon/tooldispatch
// (timestamp, event_type, agent_id, tool) plus loop-guard-specific fields.
//
// Author/Machine-Id: cursor-parent@win3-wsl3
package loopguard

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// LoopTripEvent is one NDJSON event row emitted when LoopGuard trips.
type LoopTripEvent struct {
	Timestamp string `json:"ts"`
	EventType string `json:"event_type"` // always "loopguard_trip"
	Tool      string `json:"tool"`
	Hash      string `json:"hash"`
	AgentID   string `json:"agent_id,omitempty"`
	Window    string `json:"window"` // e.g. "60s"
	Count     int    `json:"count"`  // number of identical calls in window
}

// AgentraceEmitter serializes LoopTripEvent rows to a NDJSON file.
// Safe for concurrent use.
type AgentraceEmitter struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// NewAgentraceEmitter creates or appends to the NDJSON file at path.
func NewAgentraceEmitter(path string) (*AgentraceEmitter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("loopguard: open agentrace log %q: %w", path, err)
	}
	return &AgentraceEmitter{path: path, f: f}, nil
}

// Emit writes one LoopTripEvent as a single NDJSON line.
func (e *AgentraceEmitter) Emit(ev LoopTripEvent) error {
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if ev.EventType == "" {
		ev.EventType = "loopguard_trip"
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("loopguard: marshal event: %w", err)
	}
	line = append(line, '\n')
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.f.Write(line); err != nil {
		return fmt.Errorf("loopguard: write event: %w", err)
	}
	return nil
}

// Path returns the underlying NDJSON file path.
func (e *AgentraceEmitter) Path() string { return e.path }

// Close flushes and closes the underlying file.
func (e *AgentraceEmitter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil {
		return nil
	}
	err := e.f.Close()
	e.f = nil
	return err
}
