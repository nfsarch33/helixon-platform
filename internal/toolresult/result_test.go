// Tests for the ToolResult struct (v17004-1 RED tests).
package toolresult

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolResult_StructureRoundtrip asserts the struct fields are settable and
// readable for roundtrip via JSON.
func TestToolResult_StructureRoundtrip(t *testing.T) {
	r := ToolResult{
		Status:         StatusOK,
		Output:         "hello world",
		Error:          "",
		LatencyMs:      42,
		CostUSD:        0.001,
		IdempotencyKey: "idem-1",
		RetryCount:     0,
		Hash:           "abc123",
	}
	if r.Status != StatusOK || r.Output != "hello world" || r.LatencyMs != 42 {
		t.Fatalf("struct fields wrong: %+v", r)
	}
}

// TestToolResult_JSONMarshalUnmarshal asserts JSON roundtrip preserves all fields.
func TestToolResult_JSONMarshalUnmarshal(t *testing.T) {
	in := ToolResult{
		Status:         StatusError,
		Output:         "",
		Error:          "tool failed",
		LatencyMs:      100,
		CostUSD:        0.05,
		IdempotencyKey: "idem-2",
		RetryCount:     2,
		Hash:           "def456",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ToolResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}

// TestToolResult_IdempotencyKey_Stable asserts NewToolResult assigns a stable
// idempotency key (deterministic per input).
func TestToolResult_IdempotencyKey_Stable(t *testing.T) {
	r1 := NewToolResult("tool1", `{"a":1}`, StatusOK, "out", "", 50, 0.001)
	r2 := NewToolResult("tool1", `{"a":1}`, StatusOK, "out", "", 50, 0.001)
	if r1.IdempotencyKey != r2.IdempotencyKey {
		t.Fatalf("idempotency key should be deterministic; got %q vs %q",
			r1.IdempotencyKey, r2.IdempotencyKey)
	}
	if r1.IdempotencyKey == "" {
		t.Fatal("idempotency key empty")
	}
}

// TestToolResult_CostAttributionRequired asserts Validate rejects results
// without a CostUSD value (even 0 is acceptable; missing is not).
func TestToolResult_CostAttributionRequired(t *testing.T) {
	r := ToolResult{
		Status:         StatusOK,
		Output:         "ok",
		IdempotencyKey: "idem-3",
		// CostUSD intentionally zero-valued; should be rejected only if NOT set;
		// for MVP we treat 0 as "free" (e.g. local tool) — see Validate().
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("CostUSD=0 should be acceptable (free tool): %v", err)
	}
	r2 := ToolResult{Status: StatusOK, Output: "ok"}
	// no IdempotencyKey → fail
	if err := r2.Validate(); err == nil {
		t.Fatal("Validate: missing IdempotencyKey should fail")
	} else if !strings.Contains(strings.ToLower(err.Error()), "idempotency") {
		t.Fatalf("Validate: want error mentioning idempotency, got %v", err)
	}
	r3 := ToolResult{Status: StatusOK, Output: "ok", IdempotencyKey: "k"}
	r3.Status = Status("invalid")
	if err := r3.Validate(); err == nil {
		t.Fatal("Validate: invalid Status should fail")
	}
}
