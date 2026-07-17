// Tests for v18675-3 (CF-172) tenant-id propagation through ToolResult.
// The ToolResult struct carries a TenantID field; NewToolResultWithTenant
// and ResolveTenantID are the canonical entry points.
package toolresult

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

// TestNewToolResultWithTenant_AttachesTenant verifies the helper sets
// the TenantID field on the result.
func TestNewToolResultWithTenant_AttachesTenant(t *testing.T) {
	r := NewToolResultWithTenant("echo", `{"msg":"hi"}`, StatusOK, "hi", "", 10, 0.001, "acme-corp")
	if r.TenantID != "acme-corp" {
		t.Fatalf("TenantID = %q; want %q", r.TenantID, "acme-corp")
	}
	// Other fields should still be populated.
	if r.Status != StatusOK {
		t.Fatalf("Status = %q; want %q", r.Status, StatusOK)
	}
	if r.Output != "hi" {
		t.Fatalf("Output = %q; want %q", r.Output, "hi")
	}
}

// TestNewToolResultWithTenant_EmptyTenantAllowsDefault verifies that an
// empty tenantID string falls back to "default" via ResolveTenantID.
func TestResolveTenantID_EmptyFallsBackToDefault(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "")
	got := ResolveTenantID(context.Background(), "")
	if got != "default" {
		t.Fatalf("ResolveTenantID empty = %q; want %q", got, "default")
	}
}

// TestResolveTenantID_ContextWinsOverFallback verifies the context
// tenant id takes precedence over the fallback parameter.
func TestResolveTenantID_ContextWinsOverFallback(t *testing.T) {
	ctx := tenantid.WithTenantID(context.Background(), "ctx-tenant")
	got := ResolveTenantID(ctx, "fallback-tenant")
	if got != "ctx-tenant" {
		t.Fatalf("ResolveTenantID(ctx, fallback) = %q; want %q", got, "ctx-tenant")
	}
}

// TestResolveTenantID_FallbackWinsOverEnv verifies the fallback
// parameter takes precedence over the env var when no context tenant
// is present.
func TestResolveTenantID_FallbackWinsOverEnv(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "env-tenant")
	got := ResolveTenantID(context.Background(), "fallback-tenant")
	if got != "fallback-tenant" {
		t.Fatalf("ResolveTenantID(ctx, fallback) = %q; want %q", got, "fallback-tenant")
	}
}

// TestResolveTenantID_EnvFallbackWhenNoContext verifies the env var
// fallback when neither context nor fallback parameter is set.
func TestResolveTenantID_EnvFallbackWhenNoContext(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "env-tenant")
	got := ResolveTenantID(context.Background(), "")
	if got != "env-tenant" {
		t.Fatalf("ResolveTenantID = %q; want %q", got, "env-tenant")
	}
}

// TestToolResult_TenantID_JSONRoundtrip verifies TenantID serialises to
// the JSON tag `tenant_id` and survives a roundtrip.
func TestToolResult_TenantID_JSONRoundtrip(t *testing.T) {
	in := ToolResult{
		Status:    StatusOK,
		Output:    "ok",
		LatencyMs: 5,
		TenantID:  "json-tenant",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Verify the JSON tag.
	if !contains(string(data), `"tenant_id":"json-tenant"`) {
		t.Fatalf("JSON missing tenant_id tag: %s", string(data))
	}
	var out ToolResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.TenantID != "json-tenant" {
		t.Fatalf("TenantID = %q; want %q", out.TenantID, "json-tenant")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
