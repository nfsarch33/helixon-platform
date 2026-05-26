package tooldispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// AgentraceConfig controls how a TracedExecutor records NDJSON events.
// LogPath is required; all other fields have sensible defaults.
type AgentraceConfig struct {
	LogPath string
	AgentID string
	Server  string

	Now func() time.Time
}

// TracedExecutor wraps an inner ToolExecutor (typically *Registry) and
// appends one agentrace NDJSON event per Execute call. The inner executor
// is invoked verbatim; tracing failures are logged but never block the
// underlying tool dispatch (canonical-vs-secondary asymmetry).
type TracedExecutor struct {
	inner    InnerExecutor
	cfg      AgentraceConfig
	logger   *slog.Logger
	sink     *agentraceSink
	closeMu  sync.Mutex
	closeErr error
	closed   bool
}

// InnerExecutor is the minimum surface a TracedExecutor needs to wrap.
// *Registry implements it; tests can supply mocks.
type InnerExecutor interface {
	Execute(ctx context.Context, name string, argsJSON string) (string, error)
	Available() []llm.Tool
}

// NewTracedExecutor wraps inner with NDJSON agentrace recording.
func NewTracedExecutor(inner InnerExecutor, cfg AgentraceConfig, logger *slog.Logger) (*TracedExecutor, error) {
	if inner == nil {
		return nil, errors.New("tooldispatch: TracedExecutor requires inner executor")
	}
	if cfg.LogPath == "" {
		return nil, errors.New("tooldispatch: AgentraceConfig.LogPath is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}

	sink, err := newAgentraceSink(cfg.LogPath)
	if err != nil {
		return nil, fmt.Errorf("tooldispatch: open agentrace log: %w", err)
	}

	return &TracedExecutor{
		inner:  inner,
		cfg:    cfg,
		logger: logger.With(slog.String("component", "helixon.agentrace")),
		sink:   sink,
	}, nil
}

// Execute dispatches to the inner executor and records one NDJSON event.
// A trace write failure never propagates as an Execute error; the inner
// result is always returned verbatim.
func (t *TracedExecutor) Execute(ctx context.Context, name string, argsJSON string) (string, error) {
	start := t.cfg.Now()
	result, err := t.inner.Execute(ctx, name, argsJSON)
	elapsed := t.cfg.Now().Sub(start)

	ev := agentraceEvent{
		Timestamp:  start.UTC().Format(time.RFC3339Nano),
		EventType:  "tool_call",
		Tool:       name,
		Server:     t.cfg.Server,
		AgentID:    t.cfg.AgentID,
		DurationMS: elapsed.Milliseconds(),
		Success:    err == nil,
	}
	if err != nil {
		ev.ErrorMessage = err.Error()
	}
	if writeErr := t.sink.write(ev); writeErr != nil {
		t.logger.Warn("agentrace sink write failed (non-fatal)",
			slog.String("tool", name),
			slog.String("error", writeErr.Error()),
		)
	}
	return result, err
}

// Available proxies through to the inner executor.
func (t *TracedExecutor) Available() []llm.Tool {
	return t.inner.Available()
}

// Close flushes and closes the underlying NDJSON sink. Safe to call multiple times.
func (t *TracedExecutor) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return t.closeErr
	}
	t.closed = true
	t.closeErr = t.sink.Close()
	return t.closeErr
}

// agentraceEvent matches the NDJSON schema used by sembleproxy and
// helixon-ec dailyreport so a single downstream collector can ingest both.
type agentraceEvent struct {
	Timestamp    string `json:"ts"`
	EventType    string `json:"event_type"`
	Tool         string `json:"tool,omitempty"`
	Server       string `json:"server,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
	Success      bool   `json:"success"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// agentraceSink is a mutex-guarded NDJSON appender. Concurrent writers
// serialize through mu so log lines never interleave.
type agentraceSink struct {
	mu sync.Mutex
	f  *os.File
}

func newAgentraceSink(path string) (*agentraceSink, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &agentraceSink{f: f}, nil
}

func (s *agentraceSink) write(ev agentraceEvent) error {
	if s == nil {
		return nil
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return errors.New("agentrace sink already closed")
	}
	_, err = s.f.Write(line)
	return err
}

func (s *agentraceSink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}
