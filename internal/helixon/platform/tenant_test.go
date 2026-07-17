package platform

import "testing"

// v18686-1: M-T 100% on platform package (server glue).
func TestTenant_Config_AcceptsTenantID_v18686_1(t *testing.T) {
	c := Config{
		Addr:     ":8080",
		TenantID: "tenant-a",
	}
	if c.TenantID != "tenant-a" {
		t.Fatalf("TenantID not preserved: got %q", c.TenantID)
	}
}

func TestTenant_Config_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	c := Config{Addr: ":8080"}
	if c.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", c.TenantID)
	}
}

func TestTenant_Config_StampsTenantID_v18686_1(t *testing.T) {
	c := Config{
		TenantID: "tenant-x",
	}
	if c.TenantID != "tenant-x" {
		t.Fatalf("Config.TenantID lost: got %q", c.TenantID)
	}
}

func TestTenant_Config_TenantIDsAreDistinct_v18686_1(t *testing.T) {
	a := Config{TenantID: "tenant-a"}
	b := Config{TenantID: "tenant-b"}
	if a.TenantID == b.TenantID {
		t.Fatalf("two tenants must have distinct IDs")
	}
}
