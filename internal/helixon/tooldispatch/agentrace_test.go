package tooldispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// stubExecutor is a minimal InnerExecutor for tracing tests.
type stubExecutor struct {
	mu     sync.Mutex
	calls  []string
	result string
	err    error
	tools  []llm.Tool
	delay  time.Duration
}

func (s *stubExecutor) Execute(ctx context.Context, name, argsJSON string) (string, error) { //nolint:revive // unused-parameter required by interface
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.result, s.err
}

func (s *stubExecutor) Available() []llm.Tool { return s.tools }

func tempLogPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "agentrace.ndjson")
}

func readEvents(t *testing.T, path string) []agentraceEvent {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304 test fixture
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	events := make([]agentraceEvent, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ev agentraceEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestTracedExecutor_RecordsSuccess(t *testing.T) {
	logPath := tempLogPath(t)
	stub := &stubExecutor{result: "ok"}
	fixed := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixed }
	te, err := NewTracedExecutor(stub, AgentraceConfig{
		LogPath: logPath, AgentID: "claude-code", Server: "helixon", Now: now,
	}, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor: %v", err)
	}
	t.Cleanup(func() { _ = te.Close() })

	got, err := te.Execute(context.Background(), "memory.search", `{"query":"hello"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "ok" {
		t.Fatalf("Execute result = %q, want %q", got, "ok")
	}

	if err := te.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Tool != "memory.search" {
		t.Fatalf("Tool = %q, want memory.search", ev.Tool)
	}
	if ev.AgentID != "claude-code" {
		t.Fatalf("AgentID = %q, want claude-code", ev.AgentID)
	}
	if ev.Server != "helixon" {
		t.Fatalf("Server = %q, want helixon", ev.Server)
	}
	if !ev.Success {
		t.Fatalf("Success = false, want true")
	}
	if ev.EventType != "tool_call" {
		t.Fatalf("EventType = %q, want tool_call", ev.EventType)
	}
	if ev.ErrorMessage != "" {
		t.Fatalf("ErrorMessage = %q, want empty", ev.ErrorMessage)
	}
}

func TestTracedExecutor_RecordsFailure(t *testing.T) {
	logPath := tempLogPath(t)
	stub := &stubExecutor{err: errors.New("boom")}
	te, err := NewTracedExecutor(stub, AgentraceConfig{LogPath: logPath, AgentID: "claude-code"}, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor: %v", err)
	}
	t.Cleanup(func() { _ = te.Close() })

	if _, err := te.Execute(context.Background(), "broken.tool", "{}"); err == nil {
		t.Fatalf("Execute err = nil, want error")
	}
	if err := te.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Success {
		t.Fatalf("Success = true, want false on failed Execute")
	}
	if events[0].ErrorMessage != "boom" {
		t.Fatalf("ErrorMessage = %q, want boom", events[0].ErrorMessage)
	}
}

func TestTracedExecutor_DurationCaptured(t *testing.T) {
	logPath := tempLogPath(t)
	stub := &stubExecutor{result: "ok"}

	// Synthetic clock: first Now=t0, second Now=t0+50ms.
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	calls := 0
	now := func() time.Time {
		defer func() { calls++ }()
		if calls == 0 {
			return t0
		}
		return t0.Add(50 * time.Millisecond)
	}
	te, err := NewTracedExecutor(stub, AgentraceConfig{LogPath: logPath, AgentID: "claude-code", Now: now}, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor: %v", err)
	}
	t.Cleanup(func() { _ = te.Close() })

	if _, err := te.Execute(context.Background(), "memory.search", "{}"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := te.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].DurationMS != 50 {
		t.Fatalf("DurationMS = %d, want 50", events[0].DurationMS)
	}
}

func TestTracedExecutor_AvailableProxies(t *testing.T) {
	stub := &stubExecutor{
		tools: []llm.Tool{{Type: "function", Function: llm.FunctionDef{Name: "alpha"}}},
	}
	te, err := NewTracedExecutor(stub, AgentraceConfig{LogPath: tempLogPath(t)}, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor: %v", err)
	}
	t.Cleanup(func() { _ = te.Close() })

	tools := te.Available()
	if len(tools) != 1 || tools[0].Function.Name != "alpha" {
		t.Fatalf("Available proxy mismatch: %+v", tools)
	}
}

func TestTracedExecutor_RequiresLogPath(t *testing.T) {
	stub := &stubExecutor{}
	if _, err := NewTracedExecutor(stub, AgentraceConfig{}, nil); err == nil {
		t.Fatalf("NewTracedExecutor with empty LogPath: err = nil, want error")
	}
}

func TestTracedExecutor_RequiresInner(t *testing.T) {
	if _, err := NewTracedExecutor(nil, AgentraceConfig{LogPath: tempLogPath(t)}, nil); err == nil {
		t.Fatalf("NewTracedExecutor with nil inner: err = nil, want error")
	}
}

func TestTracedExecutor_ConcurrentWritesNotInterleaved(t *testing.T) {
	logPath := tempLogPath(t)
	stub := &stubExecutor{result: "ok", delay: 1 * time.Millisecond}
	te, err := NewTracedExecutor(stub, AgentraceConfig{LogPath: logPath, AgentID: "claude-code"}, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor: %v", err)
	}
	t.Cleanup(func() { _ = te.Close() })

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = te.Execute(context.Background(), "memory.search", "{}")
		}()
	}
	wg.Wait()
	if err := te.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readEvents(t, logPath)
	if len(events) != N {
		t.Fatalf("len(events) = %d, want %d", len(events), N)
	}
	for i, ev := range events {
		if ev.Tool != "memory.search" {
			t.Fatalf("event %d Tool = %q, want memory.search (interleaved write?)", i, ev.Tool)
		}
	}
}

func TestTracedExecutor_CloseIdempotent(t *testing.T) {
	logPath := tempLogPath(t)
	te, err := NewTracedExecutor(&stubExecutor{result: "ok"}, AgentraceConfig{LogPath: logPath}, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor: %v", err)
	}
	if err := te.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := te.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestTracedExecutor_WrapsRegistry(t *testing.T) {
	// Integration: wrap a real *Registry and confirm it satisfies the agent.ToolExecutor
	// interface end-to-end via Execute.
	logPath := tempLogPath(t)
	r := NewRegistry(nil)
	if err := r.Register(ToolDef{
		Name:        "echo",
		Description: "echo back the message arg",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			s, _ := args["msg"].(string)
			return s, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	te, err := NewTracedExecutor(r, AgentraceConfig{LogPath: logPath, AgentID: "claude-code", Server: "helixon"}, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor: %v", err)
	}
	t.Cleanup(func() { _ = te.Close() })

	got, err := te.Execute(context.Background(), "echo", `{"msg":"v8000"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "v8000" {
		t.Fatalf("Execute result = %q, want v8000", got)
	}

	if err := te.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	events := readEvents(t, logPath)
	if len(events) != 1 || events[0].Tool != "echo" || !events[0].Success {
		t.Fatalf("event mismatch: %+v", events)
	}
}
