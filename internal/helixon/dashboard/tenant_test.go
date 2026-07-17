package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestTenant_AgentWorkloadFetcher_PassesTenantFilterQuery verifies the
// AgentWorkloadFetcher forwards the TenantID to the upstream sprintboard
// API as a query parameter, enabling per-tenant filtering on the server
// side. Without this, the dashboard would expose cross-tenant agent
// data, violating v18680-3 + v18684-4 multi-tenancy invariants.
func TestTenant_AgentWorkloadFetcher_PassesTenantFilterQuery(t *testing.T) {
	var capturedQuery string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"agent_id":"a1","status":"active","current_task":"T-1","tenant_id":"tenant-x"}
		]`))
	}))
	defer srv.Close()

	f := NewAgentWorkloadFetcherWithTenant(srv.URL, "tenant-x")
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedQuery, "tenant_id=tenant-x") {
		t.Errorf("expected tenant_id=tenant-x in query, got %q", capturedQuery)
	}
}

// TestTenant_AgentWorkloadFetcher_TwoTenantsDoNotLeak verifies that
// two fetchers configured for different tenants forward different
// query parameters and never see each other's data.
func TestTenant_AgentWorkloadFetcher_TwoTenantsDoNotLeak(t *testing.T) {
	var (
		mu       sync.Mutex
		tenantA  string
		tenantB  string
		counterA int
		counterB int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		q := r.URL.Query().Get("tenant_id")
		w.Header().Set("Content-Type", "application/json")
		switch q {
		case "tenant-a":
			tenantA = q
			counterA++
			_, _ = w.Write([]byte(`[{"agent_id":"a1","status":"active","tenant_id":"tenant-a"}]`))
		case "tenant-b":
			tenantB = q
			counterB++
			_, _ = w.Write([]byte(`[{"agent_id":"b1","status":"active","tenant_id":"tenant-b"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	fA := NewAgentWorkloadFetcherWithTenant(srv.URL, "tenant-a")
	fB := NewAgentWorkloadFetcherWithTenant(srv.URL, "tenant-b")

	respA, err := fA.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	respB, err := fB.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if tenantA != "tenant-a" || tenantB != "tenant-b" {
		t.Errorf("tenant filter not forwarded: A=%q B=%q", tenantA, tenantB)
	}
	if counterA == 0 || counterB == 0 {
		t.Error("one tenant never received a request")
	}

	// Each response must contain ONLY that tenant's agent.
	for _, a := range respA.Agents {
		if a.TenantID != "" && a.TenantID != "tenant-a" {
			t.Errorf("tenant-A response leaked agent with tenant_id=%q", a.TenantID)
		}
	}
	for _, a := range respB.Agents {
		if a.TenantID != "" && a.TenantID != "tenant-b" {
			t.Errorf("tenant-B response leaked agent with tenant_id=%q", a.TenantID)
		}
	}
}

// TestTenant_AgentWorkloadHandler_PassesTenantFilterQuery verifies that
// the HTTP handler forwards the X-Tenant-ID header to the fetcher.
func TestTenant_AgentWorkloadHandler_PassesTenantFilterQuery(t *testing.T) {
	var capturedQuery string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"agent_id":"a1","status":"active","tenant_id":"tenant-y"}]`))
	}))
	defer srv.Close()

	// Build a fetcher that reads X-Tenant-ID from the incoming request.
	f := NewAgentWorkloadFetcher(srv.URL)
	h := TenantAgentWorkloadHandler(f)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("X-Tenant-ID", "tenant-y")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedQuery, "tenant_id=tenant-y") {
		t.Errorf("expected tenant_id=tenant-y in query, got %q", capturedQuery)
	}

	// Sanity: the response decodes cleanly into AgentWorkloadResponse.
	var resp AgentWorkloadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Errorf("response not valid JSON: %v", err)
	}
}
