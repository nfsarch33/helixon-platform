// Tests for the LoopGuard Agentrace + metrics emit (v17003-4).
package loopguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAgentraceEmit_AppendsLoopTripEvent verifies that EmitToNDJSON appends
// one valid NDJSON event per call.
func TestAgentraceEmit_AppendsLoopTripEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "loopguard.ndjson")

	emit, err := NewAgentraceEmitter(logPath)
	if err != nil {
		t.Fatalf("NewAgentraceEmitter: %v", err)
	}
	defer func() { _ = emit.Close() }()

	if err := emit.Emit(LoopTripEvent{
		Tool:    "read_file",
		Hash:    "abc123def456",
		AgentID: "agent-001",
		Window:  "60s",
		Count:   3,
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("NDJSON log empty")
	}
	var ev LoopTripEvent
	if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if ev.Tool != "read_file" || ev.AgentID != "agent-001" || ev.Count != 3 {
		t.Fatalf("event fields wrong: %+v", ev)
	}
	if !strings.HasPrefix(ev.Timestamp, "20") {
		t.Fatalf("timestamp not RFC3339: %q", ev.Timestamp)
	}
}

// TestAgentraceEmit_ConcurrentSafe verifies that multiple goroutines can
// emit without losing lines or corrupting NDJSON.
func TestAgentraceEmit_ConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "loopguard.ndjson")

	emit, err := NewAgentraceEmitter(logPath)
	if err != nil {
		t.Fatalf("NewAgentraceEmitter: %v", err)
	}
	defer func() { _ = emit.Close() }()

	var wg sync.WaitGroup
	const N = 50
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = emit.Emit(LoopTripEvent{
				Tool:    "tool",
				Hash:    "hash",
				AgentID: "agent",
				Count:   idx,
			})
		}(i)
	}
	wg.Wait()

	data, _ := os.ReadFile(logPath)
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != N {
		t.Fatalf("lines: want %d, got %d", N, len(lines))
	}
	for i, line := range lines {
		var ev LoopTripEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
	}
}
