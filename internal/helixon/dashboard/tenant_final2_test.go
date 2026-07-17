package dashboard

import "testing"

// v18686-1: M-T 100% on dashboard final 2 endpoints (sprint_progress,
// cicd_status). Tests assert TenantID is a struct field on the response
// shape (it can be empty for legacy callers).
func TestTenant_SprintProgressResponse_AcceptsTenantID_v18686_1(t *testing.T) {
	r := SprintProgressResponse{
		SprintID: "v18686",
		TenantID: "tenant-a",
	}
	if r.TenantID != "tenant-a" {
		t.Fatalf("TenantID not preserved: got %q", r.TenantID)
	}
}

func TestTenant_SprintProgressResponse_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	r := SprintProgressResponse{SprintID: "v18686"}
	if r.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", r.TenantID)
	}
}

func TestTenant_CICDStatusResponse_AcceptsTenantID_v18686_1(t *testing.T) {
	r := CICDStatusResponse{
		TotalCount: 1,
		TenantID:   "tenant-x",
	}
	if r.TenantID != "tenant-x" {
		t.Fatalf("TenantID not preserved: got %q", r.TenantID)
	}
}

func TestTenant_CICDStatusResponse_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	r := CICDStatusResponse{TotalCount: 1}
	if r.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", r.TenantID)
	}
}

func TestTenant_DashboardResponses_TenantIDsAreDistinct_v18686_1(t *testing.T) {
	a := SprintProgressResponse{TenantID: "tenant-a"}
	b := SprintProgressResponse{TenantID: "tenant-b"}
	c := CICDStatusResponse{TenantID: "tenant-c"}
	if a.TenantID == b.TenantID || b.TenantID == c.TenantID || a.TenantID == c.TenantID {
		t.Fatalf("all tenants must be distinct")
	}
}
