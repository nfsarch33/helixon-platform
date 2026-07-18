// cross_isolation_e2e_test.go -- v18692-4 multitenancy cross-isolation E2E.
//
// Verifies the pilot cross-isolation contract:
//
//  1. 50 goroutines × 5 tenants concurrent pilot harness.
//  2. Each tenant's records MUST NOT bleed into another tenant's view.
//  3. Audit log entry per write with tenant_id + job_id.
//  4. Cross-tenant access denied (Put / Get / List).
//
// Cost discipline: no paid LLM calls; in-process store + audit log.
// CI-runnable in default `go test ./...` regression.
package multitenancy

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

// TestCrossIsolation_50x5_Concurrent exercises the pilot harness:
// 50 goroutines, each tied to one of 5 tenants, each writing 10
// records. Total: 500 records (100 per tenant). Asserts:
//
//   - Each tenant's store has exactly 100 records.
//   - No tenant can see another tenant's records (List).
//   - Get on a foreign tenant's key fails (cross-tenant denied).
//   - Audit log carries >= 500 write rows + >= 50 cross-tenant
//     attempts (the test deliberately tries a cross-tenant read).
func TestCrossIsolation_50x5_Concurrent(t *testing.T) {
	const (
		workers      = 50
		tenants      = 5
		writesPerWkr = 10
	)
	store := NewStore()
	tenantList := []string{"tenant-a", "tenant-b", "tenant-c", "tenant-d", "tenant-e"}

	var wg sync.WaitGroup
	crossAttempts := make(chan string, workers*2)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			tenant := tenantList[w%tenants]
			ctx := tenantid.WithTenantID(context.Background(), tenant)
			for j := 0; j < writesPerWkr; j++ {
				rec := Record{
					Key:      fmt.Sprintf("w%d-r%d", w, j),
					Value:    fmt.Sprintf("payload-from-%s", tenant),
					TenantID: tenant,
					JobID:    fmt.Sprintf("job-%s-w%d-r%d", tenant, w, j),
				}
				if err := store.Put(ctx, rec); err != nil {
					t.Errorf("Put failed: %v", err)
					return
				}
			}
			// Cross-tenant probe: try to read another tenant's record
			foreignTenant := tenantList[(w+1)%tenants]
			_, err := store.Get(ctx, foreignTenant, "probe")
			if err == nil {
				t.Errorf("cross-tenant Get succeeded for tenant=%s foreign=%s", tenant, foreignTenant)
			}
			crossAttempts <- tenant
		}(w)
	}
	wg.Wait()
	close(crossAttempts)

	// Assertion 1: per-tenant count
	for _, tn := range tenantList {
		got := store.Count(tn)
		want := (workers / tenants) * writesPerWkr
		if got != want {
			t.Errorf("tenant %s: count=%d, want %d", tn, got, want)
		}
	}

	// Assertion 2: cross-tenant List — each tenant sees only its own
	for _, tn := range tenantList {
		ctx := tenantid.WithTenantID(context.Background(), tn)
		rows, err := store.List(ctx)
		if err != nil {
			t.Errorf("List(%s): %v", tn, err)
			continue
		}
		for _, r := range rows {
			if r.TenantID != tn {
				t.Errorf("tenant %s saw foreign record %+v", tn, r)
			}
		}
	}

	// Assertion 3: audit log shape
	audit := store.Audit()
	var writes, reads, cross int
	for _, e := range audit {
		switch e.Event {
		case "write":
			writes++
		case "read":
			reads++
		case "cross_tenant_attempt":
			cross++
		}
	}
	if writes != workers*writesPerWkr {
		t.Errorf("audit writes=%d, want %d", writes, workers*writesPerWkr)
	}
	if cross != workers {
		t.Errorf("audit cross_tenant_attempt=%d, want %d (one per worker probe)", cross, workers)
	}
	if reads < tenants {
		t.Errorf("audit reads=%d, want >= %d (one List per tenant)", reads, tenants)
	}
	t.Logf("50x5 concurrent: writes=%d reads=%d cross_tenant=%d", writes, reads, cross)
}

// TestCrossIsolation_PutForeignCtxDenied verifies that an attempt to
// Put a record with tenant_id=X using a context carrying tenant_id=Y
// is denied with ErrCrossTenant.
func TestCrossIsolation_PutForeignCtxDenied(t *testing.T) {
	store := NewStore()
	ctx := tenantid.WithTenantID(context.Background(), "tenant-a")
	err := store.Put(ctx, Record{Key: "k", Value: "v", TenantID: "tenant-b"})
	if err == nil {
		t.Fatal("expected ErrCrossTenant, got nil")
	}
	if _, ok := err.(ErrCrossTenant); !ok {
		t.Errorf("error type=%T want ErrCrossTenant", err)
	}
	// Audit must contain the cross-tenant attempt
	for _, e := range store.Audit() {
		if e.Event == "cross_tenant_attempt" && e.Result == "denied" {
			return
		}
	}
	t.Error("audit log missing cross_tenant_attempt entry")
}

// TestCrossIsolation_GetForeignDenied — Get with foreign tenant in URL.
func TestCrossIsolation_GetForeignDenied(t *testing.T) {
	store := NewStore()
	ctxA := tenantid.WithTenantID(context.Background(), "tenant-a")
	_ = store.Put(ctxA, Record{Key: "secret", Value: "alpha", TenantID: "tenant-a", JobID: "job-1"})

	// tenant-b tries to read tenant-a's record
	ctxB := tenantid.WithTenantID(context.Background(), "tenant-b")
	_, err := store.Get(ctxB, "tenant-a", "secret")
	if err == nil {
		t.Fatal("expected ErrCrossTenant, got nil")
	}
}

// TestCrossIsolation_AuditJobID verifies each write audit entry carries
// the tenant_id AND job_id pair.
func TestCrossIsolation_AuditJobID(t *testing.T) {
	store := NewStore()
	ctx := tenantid.WithTenantID(context.Background(), "tenant-audit")
	_ = store.Put(ctx, Record{Key: "k1", Value: "v1", TenantID: "tenant-audit", JobID: "job-abc"})
	_ = store.Put(ctx, Record{Key: "k2", Value: "v2", TenantID: "tenant-audit", JobID: "job-xyz"})

	var writes []AuditEntry
	for _, e := range store.Audit() {
		if e.Event == "write" {
			writes = append(writes, e)
		}
	}
	if len(writes) != 2 {
		t.Fatalf("got %d writes, want 2", len(writes))
	}
	got := map[string]string{}
	for _, w := range writes {
		got[w.JobID] = w.TenantID
	}
	if got["job-abc"] != "tenant-audit" || got["job-xyz"] != "tenant-audit" {
		t.Errorf("audit job_id/tenant_id mismatch: %+v", got)
	}
}
