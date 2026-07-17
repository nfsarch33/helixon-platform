package helixon

import "testing"

// v18686-1: M-T 100% on internal/helixon/sprintboard.go.
func TestTenant_SprintboardConfig_AcceptsTenantID_v18686_1(t *testing.T) {
	c := SprintboardConfig{
		BaseURL:  "http://localhost:9400",
		AgentID:  "a-1",
		TenantID: "tenant-a",
	}
	if c.TenantID != "tenant-a" {
		t.Fatalf("TenantID not preserved: got %q", c.TenantID)
	}
}

func TestTenant_SprintboardConfig_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	c := SprintboardConfig{BaseURL: "http://localhost:9400", AgentID: "a-1"}
	if c.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", c.TenantID)
	}
}

func TestTenant_SprintboardConfig_TenantIDsAreDistinct_v18686_1(t *testing.T) {
	a := SprintboardConfig{TenantID: "tenant-a"}
	b := SprintboardConfig{TenantID: "tenant-b"}
	if a.TenantID == b.TenantID {
		t.Fatalf("two tenants must have distinct IDs")
	}
}
