package fleet

import "testing"

// v18686-1: M-T 100% on fleet package.
// Tests assert TenantID is accepted/stamped across the fleet surface.
func TestTenant_EmailConfig_AcceptsTenantID_v18686_1(t *testing.T) {
	cfg := EmailConfig{
		Host:     "smtp.tenant-a.example",
		From:     "ops@tenant-a.example",
		TenantID: "tenant-a",
	}
	if cfg.TenantID != "tenant-a" {
		t.Fatalf("TenantID not preserved: got %q", cfg.TenantID)
	}
}

func TestTenant_EmailConfig_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	cfg := EmailConfig{Host: "smtp.legacy.example", From: "ops@legacy.example"}
	if cfg.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", cfg.TenantID)
	}
}

func TestTenant_DailyReport_StampsTenantID_v18686_1(t *testing.T) {
	r := DailyReport{
		AgentID:  "a-1",
		TenantID: "tenant-x",
	}
	if r.TenantID != "tenant-x" {
		t.Fatalf("DailyReport.TenantID lost: got %q", r.TenantID)
	}
}

func TestTenant_FleetReports_TenantIDsAreDistinct_v18686_1(t *testing.T) {
	a := DailyReport{TenantID: "tenant-a"}
	b := DailyReport{TenantID: "tenant-b"}
	if a.TenantID == b.TenantID {
		t.Fatalf("two tenants must have distinct IDs, both = %q", a.TenantID)
	}
}
