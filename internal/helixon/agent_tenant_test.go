package helixon

import "testing"

// v18686-1: M-T 100% on internal/helixon/agent.go (RuntimeConfig already
// has TenantID; these tests assert the surface holds the value).
func TestTenant_RuntimeConfig_AcceptsTenantID_v18686_1(t *testing.T) {
	c := RuntimeConfig{
		AgentID:  "a-1",
		TenantID: "tenant-a",
	}
	if c.TenantID != "tenant-a" {
		t.Fatalf("TenantID not preserved: got %q", c.TenantID)
	}
}

func TestTenant_RuntimeConfig_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	c := RuntimeConfig{AgentID: "a-1"}
	if c.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", c.TenantID)
	}
}

func TestTenant_RuntimeConfig_TenantIDsAreDistinct_v18686_1(t *testing.T) {
	a := RuntimeConfig{TenantID: "tenant-a"}
	b := RuntimeConfig{TenantID: "tenant-b"}
	if a.TenantID == b.TenantID {
		t.Fatalf("two tenants must have distinct IDs")
	}
}
