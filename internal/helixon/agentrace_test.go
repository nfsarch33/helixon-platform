package helixon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTraceMiddleware_WritesNDJSON(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "trace.ndjson")
	tm, err := NewTraceMiddleware(TraceConfig{
		LogPath: logPath,
		AgentID: "trace-test-agent",
	})
	if err != nil {
		t.Fatalf("NewTraceMiddleware: %v", err)
	}
	defer tm.Close()

	result, err := tm.Wrap("memory.search", func() (string, error) {
		time.Sleep(5 * time.Millisecond)
		return `[{"id":"m1"}]`, nil
	})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if result != `[{"id":"m1"}]` {
		t.Errorf("result = %q, want JSON array", result)
	}

	tm.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), string(data))
	}

	var ev TraceEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.ToolName != "memory.search" {
		t.Errorf("tool_name = %q, want memory.search", ev.ToolName)
	}
	if ev.AgentID != "trace-test-agent" {
		t.Errorf("agent_id = %q, want trace-test-agent", ev.AgentID)
	}
	if !ev.Success {
		t.Error("expected success=true")
	}
	if ev.DurationMS < 5 {
		t.Errorf("duration_ms = %d, expected >= 5", ev.DurationMS)
	}
	if ev.Error != "" {
		t.Errorf("error should be empty, got %q", ev.Error)
	}
}

func TestTraceMiddleware_RecordsErrors(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "errors.ndjson")
	tm, err := NewTraceMiddleware(TraceConfig{
		LogPath: logPath,
		AgentID: "error-agent",
	})
	if err != nil {
		t.Fatalf("NewTraceMiddleware: %v", err)
	}
	defer tm.Close()

	expectedErr := errors.New("connection refused")
	_, callErr := tm.Wrap("sprintboard.claim", func() (string, error) {
		return "", expectedErr
	})
	if callErr != expectedErr {
		t.Fatalf("expected original error, got %v", callErr)
	}

	tm.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var ev TraceEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Success {
		t.Error("expected success=false for errored call")
	}
	if ev.Error != "connection refused" {
		t.Errorf("error = %q, want 'connection refused'", ev.Error)
	}
	if ev.ToolName != "sprintboard.claim" {
		t.Errorf("tool_name = %q, want sprintboard.claim", ev.ToolName)
	}
}

func TestTraceMiddleware_MultipleWrites(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "multi.ndjson")
	tm, err := NewTraceMiddleware(TraceConfig{
		LogPath: logPath,
		AgentID: "multi-agent",
	})
	if err != nil {
		t.Fatalf("NewTraceMiddleware: %v", err)
	}

	for i := 0; i < 5; i++ {
		tm.Wrap("tool_"+string(rune('a'+i)), func() (string, error) {
			return "ok", nil
		})
	}
	tm.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	for i, line := range lines {
		var ev TraceEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
		if !ev.Success {
			t.Errorf("line %d: expected success", i)
		}
		if ev.AgentID != "multi-agent" {
			t.Errorf("line %d: agent_id = %q", i, ev.AgentID)
		}
	}
}

func TestTraceMiddleware_RequiresLogPath(t *testing.T) {
	t.Parallel()

	_, err := NewTraceMiddleware(TraceConfig{AgentID: "no-path"})
	if err == nil {
		t.Fatal("expected error for empty LogPath")
	}
}

func TestTraceEvent_JSONFormat(t *testing.T) {
	t.Parallel()

	ev := TraceEvent{
		Timestamp:  time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC),
		ToolName:   "memory.add",
		AgentID:    "format-agent",
		DurationMS: 42,
		Success:    true,
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	raw := string(data)
	if !strings.Contains(raw, `"tool_name":"memory.add"`) {
		t.Errorf("missing tool_name field in JSON: %s", raw)
	}
	if !strings.Contains(raw, `"agent_id":"format-agent"`) {
		t.Errorf("missing agent_id field in JSON: %s", raw)
	}
	if !strings.Contains(raw, `"duration_ms":42`) {
		t.Errorf("missing duration_ms field in JSON: %s", raw)
	}
	if !strings.Contains(raw, `"success":true`) {
		t.Errorf("missing success field in JSON: %s", raw)
	}
	if strings.Contains(raw, `"error"`) {
		t.Errorf("error field should be omitted when empty: %s", raw)
	}
}
