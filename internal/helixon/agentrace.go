package helixon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Trace-correlation environment variables (shared fleet contract). A parent
// process propagates the active trace to spawned children via these.
const (
	envTraceID  = "AGENTRACE_TRACE_ID"
	envParentID = "AGENTRACE_PARENT_ID"
	envSystem   = "AGENTRACE_SYSTEM"
)

// TraceEvent represents a single NDJSON line written by the TraceMiddleware.
//
// It carries BOTH the legacy helixon field names (timestamp, tool_name) and
// the canonical Agentrace 12-field names (ts, event, tool, system, trace_id,
// span_id, parent_id). The canonical names are what the agentrace-bridge and
// sprinteval reducers key on, so emitting them is what lifts Helixon out of
// the "0% emitter coverage" bucket; the legacy names are retained for
// backward compatibility with existing consumers.
type TraceEvent struct {
	// Legacy fields (retained).
	Timestamp time.Time `json:"timestamp"`
	ToolName  string    `json:"tool_name"`

	// Canonical Agentrace fields.
	TS       string `json:"ts"`
	Event    string `json:"event"`
	System   string `json:"system,omitempty"`
	AgentID  string `json:"agent_id"`
	TraceID  string `json:"trace_id,omitempty"`
	SpanID   string `json:"span_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Tool     string `json:"tool"`

	DurationMS int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

// TraceMiddleware wraps tool call execution and logs each invocation as
// an NDJSON line to a configured file path. It is safe for concurrent use.
type TraceMiddleware struct {
	mu       sync.Mutex
	file     *os.File
	agentID  string
	system   string
	traceID  string
	parentID string
	encoder  *json.Encoder
}

// TraceConfig configures the NDJSON trace middleware.
type TraceConfig struct {
	LogPath string
	AgentID string
	// System names the emitting service (defaults to $AGENTRACE_SYSTEM, or
	// "helixon"). TraceID/ParentID default to the inherited env values so a
	// fleet run is correlatable end-to-end; an empty TraceID mints a fresh id.
	System   string
	TraceID  string
	ParentID string
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

	system := cfg.System
	if system == "" {
		if system = os.Getenv(envSystem); system == "" {
			system = "helixon"
		}
	}
	traceID := cfg.TraceID
	if traceID == "" {
		if traceID = os.Getenv(envTraceID); traceID == "" {
			traceID = newTraceID()
		}
	}
	parentID := cfg.ParentID
	if parentID == "" {
		parentID = os.Getenv(envParentID)
	}

	return &TraceMiddleware{
		file:     f,
		agentID:  cfg.AgentID,
		system:   system,
		traceID:  traceID,
		parentID: parentID,
		encoder:  json.NewEncoder(f),
	}, nil
}

// newSpanID returns a 16-hex (8-byte) span id (W3C/OTel width).
func newSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// newTraceID returns a 26-char Crockford-base32 ULID for trace roots
// (48-bit ms timestamp + 80 bits of entropy, MSB-first 5-bit encoding).
func newTraceID() string {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ms := uint64(time.Now().UnixMilli())
	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	_, _ = rand.Read(raw[6:])

	out := make([]byte, 26)
	bitBuf := 0
	bitCount := 0
	bi := 0
	for oi := 0; oi < 26; oi++ {
		for bitCount < 5 && bi < 16 {
			bitBuf = (bitBuf << 8) | int(raw[bi])
			bi++
			bitCount += 8
		}
		if bitCount < 5 {
			bitBuf <<= (5 - bitCount)
			bitCount = 5
		}
		bitCount -= 5
		out[oi] = crockford[(bitBuf>>bitCount)&0x1f]
	}
	return string(out)
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
		TS:         start.UTC().Format(time.RFC3339),
		Event:      "tool_call",
		System:     tm.system,
		Tool:       toolName,
		AgentID:    tm.agentID,
		TraceID:    tm.traceID,
		SpanID:     newSpanID(),
		ParentID:   tm.parentID,
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
