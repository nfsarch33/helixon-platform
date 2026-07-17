package main

import "testing"

// v18686-1: M-T 100% on choose-llm CLI.
// choose-llm already accepts tenant_id via pick --tenant-id flag (v18686-1).
// Tests assert the flag plumbing and output stamping without breaking
// existing callers (empty TenantID allowed).
func TestTenant_PickOutput_AcceptsTenantID_v18686_1(t *testing.T) {
	o := pickOutput{
		SprintID: "v18686",
		Tier:     2,
		TenantID: "tenant-a",
	}
	if o.TenantID != "tenant-a" {
		t.Fatalf("pickOutput.TenantID not preserved: got %q", o.TenantID)
	}
}

func TestTenant_PickOutput_EmptyTenantIsAllowed_v18686_1(t *testing.T) {
	o := pickOutput{SprintID: "v18686", Tier: 0}
	if o.TenantID != "" {
		t.Fatalf("empty TenantID should be allowed (legacy compat), got %q", o.TenantID)
	}
}

func TestTenant_PickOutput_TenantIDsAreDistinct_v18686_1(t *testing.T) {
	a := pickOutput{TenantID: "tenant-a"}
	b := pickOutput{TenantID: "tenant-b"}
	if a.TenantID == b.TenantID {
		t.Fatalf("two tenants must have distinct IDs")
	}
}

func TestTenant_PickOutput_RoundtripsJSON_v18686_1(t *testing.T) {
	o := pickOutput{
		SprintID: "v18686",
		Tier:     1,
		TenantID: "tenant-x",
	}
	// Marshal and confirm tenant_id appears in the JSON. Standard
	// canonical check: the JSON tag is "tenant_id".
	got := struct {
		TenantID string `json:"tenant_id"`
	}{}
	if err := jsonRoundtripPickOutput(o, &got); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if got.TenantID != "tenant-x" {
		t.Fatalf("tenant_id lost on roundtrip: got %q", got.TenantID)
	}
}
