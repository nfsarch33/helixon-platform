package helixon

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// TraceEvent represents a single NDJSON line written by the TraceMiddleware.
type TraceEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	ToolName   string    `json:"tool_name"`
	AgentID    string    `json:"agent_id"`
	DurationMS int64     `json:"duration_ms"`
	Success    bool      `json:"success"`
	Error      string    `json:"error,omitempty"`
}

// TraceMiddleware wraps tool call execution and logs each invocation as
// an NDJSON line to a configured file path. It is safe for concurrent use.
type TraceMiddleware struct {
	mu      sync.Mutex
	file    *os.File
	agentID string
	encoder *json.Encoder
}

// TraceConfig configures the NDJSON trace middleware.
type TraceConfig struct {
	LogPath string
	AgentID string
}

// NewTraceMiddleware creates a TraceMiddleware that appends NDJSON events
// to the specified log file. The file is created if it doesn't exist.
func NewTraceMiddleware(cfg TraceConfig) (*TraceMiddleware, error) {
	if cfg.LogPath == "" {
		return nil, fmt.Errorf("agentrace: LogPath is required")
	}

	f, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("agentrace: open log: %w", err)
	}

	return &TraceMiddleware{
		file:    f,
		agentID: cfg.AgentID,
		encoder: json.NewEncoder(f),
	}, nil
}

// ToolCallFunc is the signature of a tool invocation that the middleware wraps.
type ToolCallFunc func() (string, error)

// Wrap executes fn and records the call as a TraceEvent. The event is
// written regardless of whether fn succeeds or fails.
func (tm *TraceMiddleware) Wrap(toolName string, fn ToolCallFunc) (string, error) {
	start := time.Now()
	result, callErr := fn()
	duration := time.Since(start)

	event := TraceEvent{
		Timestamp:  start.UTC(),
		ToolName:   toolName,
		AgentID:    tm.agentID,
		DurationMS: duration.Milliseconds(),
		Success:    callErr == nil,
	}
	if callErr != nil {
		event.Error = callErr.Error()
	}

	tm.mu.Lock()
	_ = tm.encoder.Encode(event)
	tm.mu.Unlock()

	return result, callErr
}

// Close flushes and closes the underlying log file.
func (tm *TraceMiddleware) Close() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.file != nil {
		return tm.file.Close()
	}
	return nil
}
