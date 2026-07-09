// Tests for the checkpoint emitter (v17007-1 RED tests).
package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestCheckpoint_OnToolCall_FiresAfterN asserts the N-tool-call threshold.
func TestCheckpoint_OnToolCall_FiresAfterN(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")

	e := New(Config{EveryNToolCalls: 5, EveryTMinutes: 1 * time.Hour, OutputPath: logPath})
	for i := 0; i < 5; i++ {
		if err := e.OnToolCall(); err != nil {
			t.Fatalf("OnToolCall %d: %v", i+1, err)
		}
	}
	// 5th call should fire.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected NDJSON line; got 0 bytes")
	}
	var cp Checkpoint
	if err := json.Unmarshal(bytesTrimSpace(data), &cp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cp.Event != "agentrace_checkpoint" {
		t.Fatalf("Event: want agentrace_checkpoint, got %q", cp.Event)
	}
}

// TestCheckpoint_ConcurrentEmits asserts concurrent safety.
func TestCheckpoint_ConcurrentEmits(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 1, OutputPath: logPath})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.OnToolCall()
		}()
	}
	wg.Wait()
}

// TestCheckpoint_ForceEmits asserts Force() emits immediately.
func TestCheckpoint_ForceEmits(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 999, OutputPath: logPath})

	if err := e.Force(); err != nil {
		t.Fatalf("Force: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	if len(data) == 0 {
		t.Fatal("Force should emit; got 0 bytes")
	}
}

// TestCheckpoint_CarriesSignal asserts the carry signal is included.
func TestCheckpoint_CarriesSignal(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 1, OutputPath: logPath})
	e.SetSignal(SignalPartialSave)
	_ = e.OnToolCall()

	data, _ := os.ReadFile(logPath)
	var cp Checkpoint
	if err := json.Unmarshal(bytesTrimSpace(data), &cp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cp.CarrySignal != SignalPartialSave {
		t.Fatalf("CarrySignal: want partial_save, got %q", cp.CarrySignal)
	}
}

// bytesTrimSpace is a small helper to avoid importing bytes just for the test.
func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\n' || b[start] == '\r') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\n' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}
