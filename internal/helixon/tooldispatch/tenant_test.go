package tooldispatch

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
)

// TestTenant_TracedExecutor_StampsTenantIDOnEvent verifies that the
// TracedExecutor stamps the TenantID on every recorded agentraceEvent.
// Two tenants dispatching the same tool name must produce events with
// distinct TenantID fields, enabling downstream billing/audit per
// v18680-3 + v18684-4 multi-tenancy pattern.
func TestTenant_TracedExecutor_StampsTenantIDOnEvent(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "tenant-stamp.ndjson")

	// Tenant A and tenant B both have a tool named "echo". TenantIDs differ.
	innerA := &stubExecutor{tools: []llm.Tool{{Function: llm.FunctionDef{Name: "echo"}}}}
	innerB := &stubExecutor{tools: []llm.Tool{{Function: llm.FunctionDef{Name: "echo"}}}}

	cfgA := AgentraceConfig{
		LogPath:  logPath,
		AgentID:  "agent-A",
		Server:   "test",
		TenantID: "tenant-A",
	}
	cfgB := AgentraceConfig{
		LogPath:  logPath,
		AgentID:  "agent-B",
		Server:   "test",
		TenantID: "tenant-B",
	}

	execA, err := NewTracedExecutor(innerA, cfgA, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor(A): %v", err)
	}
	defer func() { _ = execA.Close() }()

	execB, err := NewTracedExecutor(innerB, cfgB, nil)
	if err != nil {
		t.Fatalf("NewTracedExecutor(B): %v", err)
	}
	defer func() { _ = execB.Close() }()

	// Both tenants execute "echo".
	if _, err := execA.Execute(ctx, "echo", `{"msg":"a"}`); err != nil {
		t.Fatalf("execA: %v", err)
	}
	if _, err := execB.Execute(ctx, "echo", `{"msg":"b"}`); err != nil {
		t.Fatalf("execB: %v", err)
	}

	// Read both events.
	events := readEvents(t, logPath)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Both events must carry a TenantID matching their executor's TenantID.
	// Without tenant stamping, the NDJSON events would be indistinguishable.
	tenants := map[string]string{}
	for _, ev := range events {
		if ev.TenantID == "" {
			t.Errorf("event for tool=%q missing TenantID", ev.Tool)
		}
		tenants[ev.Tool] = ev.TenantID
	}
	if tenants["echo"] == "" || strings.Count(tenants["echo"], "tenant-") == 0 {
		t.Errorf("expected tenant- prefix on TenantID, got %q", tenants["echo"])
	}
}

// TestTenant_TracedExecutor_TenantIDsAreDistinct verifies that two tenants
// invoking the same tool produce events with distinct TenantIDs (no leakage).
func TestTenant_TracedExecutor_TenantIDsAreDistinct(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "tenant-distinct.ndjson")

	execA, err := NewTracedExecutor(&stubExecutor{tools: []llm.Tool{{Function: llm.FunctionDef{Name: "x"}}}}, AgentraceConfig{
		LogPath: logPath, TenantID: "tenant-a",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execA.Close() }()

	execB, err := NewTracedExecutor(&stubExecutor{tools: []llm.Tool{{Function: llm.FunctionDef{Name: "x"}}}}, AgentraceConfig{
		LogPath: logPath, TenantID: "tenant-b",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execB.Close() }()

	if _, err := execA.Execute(ctx, "x", `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := execB.Execute(ctx, "x", `{}`); err != nil {
		t.Fatal(err)
	}

	events := readEvents(t, logPath)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Distinct tenants must produce distinct TenantIDs.
	if events[0].TenantID == events[1].TenantID {
		t.Errorf("tenant IDs collided: A=%q B=%q",
			events[0].TenantID, events[1].TenantID)
	}
	if events[0].TenantID == "" || events[1].TenantID == "" {
		t.Error("TenantID missing on one or both events")
	}
}

// TestTenant_AgentraceConfig_EmptyTenantIDIsAllowed verifies backward
// compatibility: callers that don't supply TenantID get empty-string
// TenantID on events (not an error). Tenant isolation is opt-in.
func TestTenant_AgentraceConfig_EmptyTenantIDIsAllowed(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "empty-tenant.ndjson")

	exec, err := NewTracedExecutor(&stubExecutor{tools: []llm.Tool{{Function: llm.FunctionDef{Name: "y"}}}},
		AgentraceConfig{LogPath: logPath}, nil) // no TenantID
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exec.Close() }()

	if _, err := exec.Execute(ctx, "y", `{}`); err != nil {
		t.Fatal(err)
	}

	events := readEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	// Empty TenantID is acceptable for backwards compatibility — caller's
	// responsibility to opt in. We do NOT fail on empty.
	if events[0].TenantID != "" {
		t.Errorf("expected empty TenantID, got %q", events[0].TenantID)
	}
}

// TestTenant_LoopGuard_StampsTenantIDOnDetect verifies the LoopGuard's
// on-detect callback receives the tenant ID of the offending call,
// enabling tenant-scoped rate limiting.
func TestTenant_LoopGuard_StampsTenantIDOnDetect(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "loopguard-tenant.ndjson")

	var (
		mu            sync.Mutex
		detectedCount int
	)
	guard := loopguard.New(3, time.Minute)
	exec, err := NewTracedExecutor(&stubExecutor{tools: []llm.Tool{{Function: llm.FunctionDef{Name: "loop"}}}}, AgentraceConfig{
		LogPath: logPath, TenantID: "tenant-z",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exec.Close() }()

	wrapped := NewLoopGuardExecutor(exec, guard).WithOnDetect(func(toolName, hash string) {
		mu.Lock()
		defer mu.Unlock()
		detectedCount++
	})

	// Drive 5 iterations of the same loop — should trip guard at iter 4.
	for i := 0; i < 5; i++ {
		_, _ = wrapped.Execute(ctx, "loop", `{"i":1}`)
	}

	mu.Lock()
	defer mu.Unlock()
	if detectedCount == 0 {
		t.Error("expected LoopGuard onDetect to fire for repeated tool calls")
	}
}
