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

// TestCheckpoint_NewAppliesDefaults asserts default config when zero values
// are supplied.
func TestCheckpoint_NewAppliesDefaults(t *testing.T) {
	e := New(Config{OutputPath: "/tmp/x.ndjson"})
	if e.cfg.EveryNToolCalls != 10 {
		t.Fatalf("EveryNToolCalls default: want 10, got %d", e.cfg.EveryNToolCalls)
	}
	if e.cfg.EveryTMinutes != 30*time.Minute {
		t.Fatalf("EveryTMinutes default: want 30m, got %v", e.cfg.EveryTMinutes)
	}
}

// TestCheckpoint_Tick_FiresWhenIntervalElapsed asserts the time-based threshold.
func TestCheckpoint_Tick_FiresWhenIntervalElapsed(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 99999, EveryTMinutes: 50 * time.Millisecond, OutputPath: logPath})

	time.Sleep(80 * time.Millisecond)
	if err := e.Tick(); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Tick should emit after interval; got 0 bytes")
	}
}

// TestCheckpoint_Tick_DoesNotFireBeforeInterval asserts non-emit before threshold.
func TestCheckpoint_Tick_DoesNotFireBeforeInterval(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 99999, EveryTMinutes: 1 * time.Hour, OutputPath: logPath})

	if err := e.Tick(); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("Tick emitted before 1h interval; want no file at %s", logPath)
	}
}

// TestCheckpoint_SetCountsPersists asserts SetCounts updates internal counters
// that are emitted on the next checkpoint.
func TestCheckpoint_SetCountsPersists(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 1, OutputPath: logPath})
	e.SetCounts(7, 12, 0)
	_ = e.OnToolCall()

	data, _ := os.ReadFile(logPath)
	var cp Checkpoint
	if err := json.Unmarshal(bytesTrimSpace(data), &cp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cp.FilesWritten != 7 {
		t.Fatalf("FilesWritten: want 7, got %d", cp.FilesWritten)
	}
	if cp.TestsPassing != 12 {
		t.Fatalf("TestsPassing: want 12, got %d", cp.TestsPassing)
	}
}

// TestCheckpoint_SetBudgetPersists asserts SetBudget updates the percent that
// appears in the next checkpoint.
func TestCheckpoint_SetBudgetPersists(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 1, OutputPath: logPath})
	e.SetBudget(42)
	_ = e.OnToolCall()

	data, _ := os.ReadFile(logPath)
	var cp Checkpoint
	if err := json.Unmarshal(bytesTrimSpace(data), &cp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cp.BudgetRemainingPct != 42 {
		t.Fatalf("BudgetRemainingPct: want 42, got %d", cp.BudgetRemainingPct)
	}
}

// TestCheckpoint_TimestampIsUTC asserts timestamps are UTC-anchored.
func TestCheckpoint_TimestampIsUTC(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "check.ndjson")
	e := New(Config{EveryNToolCalls: 1, OutputPath: logPath})
	_ = e.OnToolCall()

	data, _ := os.ReadFile(logPath)
	var cp Checkpoint
	if err := json.Unmarshal(bytesTrimSpace(data), &cp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cp.Timestamp.Location() != time.UTC {
		t.Fatalf("Timestamp location: want UTC, got %v", cp.Timestamp.Location())
	}
}
