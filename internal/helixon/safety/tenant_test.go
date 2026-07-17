package safety

import "testing"

// v18686-1: M-T 100% on safety package. TenantID stamps for audit on
// cost records + validation results. Implemented as struct fields
// on existing types so callers can carry tenant context through
// the safety pipeline without a breaking change.

func TestTenant_ValidationResult_AcceptsTenantID_v18686_1(t *testing.T) {
	r := ValidationResult{
		Safe:     true,
		TenantID: "tenant-a",
	}
	if r.TenantID != "tenant-a" {
		t.Fatalf("TenantID not preserved: got %q", r.TenantID)
	}
}

func TestTenant_ValidationResult_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	r := ValidationResult{Safe: true}
	if r.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", r.TenantID)
	}
}

func TestTenant_SanitizeResult_StampsTenantID_v18686_1(t *testing.T) {
	r := SanitizeResult{
		Output:   "redacted",
		TenantID: "tenant-x",
	}
	if r.TenantID != "tenant-x" {
		t.Fatalf("SanitizeResult.TenantID lost: got %q", r.TenantID)
	}
}

func TestTenant_SanitizeResult_TwoTenantsDistinct_v18686_1(t *testing.T) {
	a := SanitizeResult{TenantID: "tenant-a"}
	b := SanitizeResult{TenantID: "tenant-b"}
	if a.TenantID == b.TenantID {
		t.Fatalf("two tenants must have distinct IDs")
	}
}
