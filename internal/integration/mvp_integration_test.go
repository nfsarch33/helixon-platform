// Package integration_test wires together the 6 MVP packages from
// Sprints 3-5 (LoopGuard, ToolResult, CircuitBreaker, Persistence,
// Checkpoint, Agentrace) into a single end-to-end flow so v17408-4
// can assert that the components compose cleanly.
//
// This file lives under package integration_test (external test
// package) so it can import the MVP packages and exercise them as
// a black box.
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/agent/checkpoint"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
	"github.com/nfsarch33/helixon-platform/internal/persistence"
	"github.com/nfsarch33/helixon-platform/internal/resilience"
	"github.com/nfsarch33/helixon-platform/internal/toolresult"
)

// fakeTool simulates a downstream provider that fails N times then succeeds.
type fakeTool struct {
	calls      int32
	failNTimes int32
}

func (f *fakeTool) Invoke() error {
	n := atomic.AddInt32(&f.calls, 1)
	if n <= atomic.LoadInt32(&f.failNTimes) {
		return resilience.ErrUpstream5xx
	}
	return nil
}

func (f *fakeTool) Calls() int { return int(atomic.LoadInt32(&f.calls)) }

// TestMVPIntegration_FullStack walks: LoopGuard → ToolResult → CircuitBreaker
// → Persistence → Checkpoint in a single test, asserting the contracts hold.
func TestMVPIntegration_FullStack(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agentrace-checkpoint.ndjson")

	// 1. LoopGuard — detector for tool-call loops (New(threshold, window)).
	guard := loopguard.New(3, 60*time.Second)
	defer guard.Reset()
	if err := guard.Observe("web-search-001"); err != nil && !errors.Is(err, loopguard.ErrLoopDetected) {
		t.Fatalf("Observe unexpected err: %v", err)
	}

	// 2. ToolResult — typed result envelope via NewToolResult.
	res := toolresult.NewToolResult("web-search", `{"q":"helixon"}`, toolresult.StatusOK, "answer=42", "", 50, 0.001)
	if res.Status != toolresult.StatusOK {
		t.Fatalf("ToolResult: want OK, got %s", res.Status)
	}
	if res.IdempotencyKey == "" {
		t.Fatal("ToolResult.IdempotencyKey should be set")
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// 3. CircuitBreaker — fail-fast after N consecutive 5xx.
	tool := &fakeTool{failNTimes: 4}
	br := resilience.NewBreaker(resilience.BreakerConfig{
		Name: "fake-tool", FailureThreshold: 2, OpenTimeout: time.Second, HalfOpenSuccesses: 1,
	})
	invoke := resilience.WrapCall(br, tool.Invoke)

	for i := 0; i < 2; i++ {
		if err := invoke(); !errors.Is(err, resilience.ErrUpstream5xx) {
			t.Fatalf("call %d: want ErrUpstream5xx, got %v", i, err)
		}
	}
	if !br.IsOpen() {
		t.Fatalf("breaker should be open after 2 failures; got %s", br.State())
	}
	if err := invoke(); !errors.Is(err, resilience.ErrBreakerOpen) {
		t.Fatalf("want ErrBreakerOpen on already-open; got %v", err)
	}

	// 4. Persistence — checkpoint the session state via InMemory backend.
	store := persistence.NewPersist(persistence.NewInMemoryBackend())
	if err := store.Save(context.Background(), persistence.State{
		Version:   1,
		AgentID:   "session-1",
		SessionID: "sess-001",
		KVState:   map[string]any{"step": 1.0, "tool_name": res.IdempotencyKey},
		MachineID: "win3-wsl3",
		SprintID:  "v17408",
	}); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	loaded, err := store.Resume(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("store.Resume: %v", err)
	}
	if loaded.KVState["step"].(float64) != 1 {
		t.Fatalf("persistence round-trip: step != 1, got %v", loaded.KVState["step"])
	}

	// 5. Checkpoint — emit an Agentrace event tied to the same flow.
	cpe := checkpoint.New(checkpoint.Config{
		EveryNToolCalls: 1,
		EveryTMinutes:   1 * time.Hour,
		OutputPath:      logPath,
	})
	cpe.SetCounts(1, 5, 0)
	cpe.SetBudget(80)
	if err := cpe.Force(); err != nil {
		t.Fatalf("checkpoint.Force: %v", err)
	}
	data, err := os.ReadFile(logPath) //nolint:gosec // G304 test fixture
	if err != nil {
		t.Fatalf("ReadFile checkpoint: %v", err)
	}
	var cp checkpoint.Checkpoint
	if err := json.Unmarshal(bytesTrimSpace(data), &cp); err != nil {
		t.Fatalf("Unmarshal checkpoint: %v", err)
	}
	if cp.FilesWritten != 1 || cp.TestsPassing != 5 || cp.BudgetRemainingPct != 80 {
		t.Fatalf("checkpoint fields wrong: %+v", cp)
	}

	// 6. LoopGuard — third identical observation should flag a loop.
	for i := 0; i < 2; i++ {
		_ = guard.Observe("dup-tool")
	}
	if err := guard.Observe("dup-tool"); !errors.Is(err, loopguard.ErrLoopDetected) {
		t.Fatalf("want ErrLoopDetected on 3rd identical hash, got %v", err)
	}
}

// TestMVPIntegration_ToolResultErrorEnvelope asserts error result
// surface returns the typed envelope.
func TestMVPIntegration_ToolResultErrorEnvelope(t *testing.T) {
	res := toolresult.NewToolResult("write-file", `{}`, toolresult.StatusError, "", "disk full", 10, 0)
	if res.Status != toolresult.StatusError {
		t.Fatalf("want StatusError, got %s", res.Status)
	}
	if !strings.Contains(res.Error, "disk full") {
		t.Fatalf("Error: want 'disk full', got %q", res.Error)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("Validate error result: %v", err)
	}
	if wrap := res.WrapErr(); wrap == nil || !strings.Contains(wrap.Error(), "disk full") {
		t.Fatalf("WrapErr: want 'disk full' message, got %v", wrap)
	}
}

// TestMVPIntegration_PersistenceConcurrentSaves asserts no data loss
// under parallel saves (last writer wins).
func TestMVPIntegration_PersistenceConcurrentSaves(t *testing.T) {
	store := persistence.NewPersist(persistence.NewInMemoryBackend())

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = store.Save(context.Background(), persistence.State{
				AgentID:   "session-1",
				SessionID: "sess-conc",
				KVState:   map[string]any{"step": float64(i)},
				MachineID: "win3-wsl3",
			})
		}(i)
	}
	wg.Wait()

	got, err := store.Resume(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, ok := got.KVState["step"]; !ok {
		t.Fatalf("missing step after concurrent saves: %+v", got.KVState)
	}
}

// bytesTrimSpace is a local helper to avoid an extra `bytes` import.
func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\n' || b[start] == '\r') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\n' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}
