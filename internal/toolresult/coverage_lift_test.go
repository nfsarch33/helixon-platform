package toolresult

import (
	"strings"
	"testing"
)

// v17702-1 coverage lift tests for toolresult. The base was 73.3%.
// These tests target Validate() and WrapErr() boundary paths that
// the original suite skipped. Each test names the path it
// exercises so coverage diffs attribute the lift cleanly.

func TestValidate_StatusErrorRequiresError(t *testing.T) {
	t.Parallel()
	r := ToolResult{Status: StatusError, IdempotencyKey: "k"}
	err := r.Validate()
	if err == nil {
		t.Fatal("StatusError with empty Error field must fail validation")
	}
	if !strings.Contains(err.Error(), "StatusError") {
		t.Fatalf("validate error %q must mention StatusError", err.Error())
	}
}

func TestValidate_StatusOKAcceptsEmptyError(t *testing.T) {
	t.Parallel()
	r := ToolResult{Status: StatusOK, IdempotencyKey: "k"}
	if err := r.Validate(); err != nil {
		t.Fatalf("StatusOK with empty Error must validate: %v", err)
	}
}

func TestWrapErr_StatusErrorReturnsError(t *testing.T) {
	t.Parallel()
	r := ToolResult{Status: StatusError, Error: "boom"}
	err := r.WrapErr()
	if err == nil {
		t.Fatal("StatusError with Error must return non-nil error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("WrapErr error %q must contain Error field", err.Error())
	}
}

func TestWrapErr_StatusErrorEmptyErrorReturnsNil(t *testing.T) {
	t.Parallel()
	r := ToolResult{Status: StatusError, Error: ""}
	if err := r.WrapErr(); err != nil {
		t.Fatalf("StatusError with empty Error must return nil from WrapErr: %v", err)
	}
}

func TestWrapErr_StatusOKReturnsNil(t *testing.T) {
	t.Parallel()
	r := ToolResult{Status: StatusOK, Error: "should not surface"}
	if err := r.WrapErr(); err != nil {
		t.Fatalf("StatusOK must always return nil from WrapErr: %v", err)
	}
}

func TestNewToolResult_FillsAllFields(t *testing.T) {
	t.Parallel()
	r := NewToolResult("echo", `{"msg":"hi"}`, StatusOK, "hi", "", 25, 0.0005)
	if r.Status != StatusOK {
		t.Fatalf("Status=%q", r.Status)
	}
	if r.Output != "hi" {
		t.Fatalf("Output=%q", r.Output)
	}
	if r.LatencyMs != 25 {
		t.Fatalf("LatencyMs=%d", r.LatencyMs)
	}
	if r.CostUSD != 0.0005 {
		t.Fatalf("CostUSD=%v", r.CostUSD)
	}
	if r.IdempotencyKey == "" {
		t.Fatal("IdempotencyKey must be populated")
	}
	if r.Hash == "" {
		t.Fatal("Hash must be populated")
	}
	// 16 hex chars per field = 8 bytes of digest.
	if len(r.IdempotencyKey) != 16 {
		t.Fatalf("IdempotencyKey len=%d, want 16", len(r.IdempotencyKey))
	}
	if len(r.Hash) != 16 {
		t.Fatalf("Hash len=%d, want 16", len(r.Hash))
	}
}

func TestNewToolResult_DifferentArgsDifferentKey(t *testing.T) {
	t.Parallel()
	a := NewToolResult("echo", `{"msg":"a"}`, StatusOK, "a", "", 1, 0)
	b := NewToolResult("echo", `{"msg":"b"}`, StatusOK, "b", "", 1, 0)
	if a.IdempotencyKey == b.IdempotencyKey {
		t.Fatal("args differ → idempotency key must differ")
	}
	if a.Hash == b.Hash {
		t.Fatal("output differs → content hash must differ")
	}
}

func TestNewToolResult_DifferentToolsDifferentKey(t *testing.T) {
	t.Parallel()
	a := NewToolResult("tool-a", `{}`, StatusOK, "x", "", 1, 0)
	b := NewToolResult("tool-b", `{}`, StatusOK, "x", "", 1, 0)
	if a.IdempotencyKey == b.IdempotencyKey {
		t.Fatal("tool name differs → idempotency key must differ")
	}
}

func TestWrapErr_ErrorImplementsError(t *testing.T) {
	t.Parallel()
	r := ToolResult{Status: StatusError, Error: "x"}
	err := r.WrapErr()
	if err == nil {
		t.Fatal("WrapErr must return non-nil error for StatusError")
	}
	if err.Error() != "x" {
		t.Fatalf("err.Error()=%q want %q", err.Error(), "x")
	}
}
