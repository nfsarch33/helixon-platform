package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestTenant_SprintboardClient_RegisterStampsTenantID verifies that
// SprintboardClient.Register carries the TenantID so the sprintboard
// can route per-tenant handoffs (v18685-2 sprintboard feature parity).
func TestTenant_SprintboardClient_RegisterStampsTenantID(t *testing.T) {
	var capturedBody []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		capturedBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(capturedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewSprintboardClient(SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "agent-1",
		TenantID:  "tenant-c",
	}, nil)
	if err := c.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var reg AgentRegistration
	if err := json.Unmarshal(capturedBody, &reg); err != nil {
		t.Fatalf("unmarshal reg: %v", err)
	}
	if reg.TenantID != "tenant-c" {
		t.Errorf("registration TenantID: want tenant-c, got %q", reg.TenantID)
	}
}

// TestTenant_SprintboardClient_ClaimStampsTenantID verifies that
// ClaimTicket carries the TenantID so ticket claims are scoped per
// tenant (cross-tenant claim is impossible).
func TestTenant_SprintboardClient_ClaimStampsTenantID(t *testing.T) {
	var capturedBody []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		capturedBody = buf
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewSprintboardClient(SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "agent-1",
		TenantID:  "tenant-d",
	}, nil)
	if err := c.ClaimTicket(context.Background(), "T-100"); err != nil {
		t.Fatalf("ClaimTicket: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var claim map[string]string
	if err := json.Unmarshal(capturedBody, &claim); err != nil {
		t.Fatalf("unmarshal claim: %v", err)
	}
	if claim["tenant_id"] != "tenant-d" {
		t.Errorf("claim tenant_id: want tenant-d, got %q", claim["tenant_id"])
	}
}
