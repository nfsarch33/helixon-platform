package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestTenant_HeartbeatPayload_StampsTenantID verifies the HeartbeatPayload
// carries the TenantID so the control plane can route per-tenant billing
// and SLO enforcement (v18685-1 + v18684-4 multi-tenancy pattern).
func TestTenant_HeartbeatPayload_StampsTenantID(t *testing.T) {
	sink := &testHeartbeatSink{}
	cfg := HeartbeatConfig{
		Interval: 10 * time.Millisecond,
		AgentID:  "agent-A",
		TenantID: "tenant-x",
	}
	m := NewHeartbeatMonitor(sink, cfg)
	defer func() { _ = m.sink }()

	if err := m.SendNow(context.Background()); err != nil {
		t.Fatalf("SendNow: %v", err)
	}

	hb := sink.last()
	if hb.AgentID != "agent-A" {
		t.Errorf("AgentID: want agent-A, got %q", hb.AgentID)
	}
	if hb.TenantID != "tenant-x" {
		t.Errorf("TenantID: want tenant-x, got %q", hb.TenantID)
	}
}

// TestTenant_HeartbeatMonitor_TwoTenantsNoLeakage verifies that two
// HeartbeatMonitor instances with different TenantIDs produce payloads
// with distinct tenant fields. The test prevents cross-tenant leakage
// at the boundary where heartbeats fan out to the control plane.
func TestTenant_HeartbeatMonitor_TwoTenantsNoLeakage(t *testing.T) {
	sinkA := &testHeartbeatSink{}
	sinkB := &testHeartbeatSink{}

	mA := NewHeartbeatMonitor(sinkA, HeartbeatConfig{Interval: 10 * time.Millisecond, AgentID: "A", TenantID: "tenant-a"})
	mB := NewHeartbeatMonitor(sinkB, HeartbeatConfig{Interval: 10 * time.Millisecond, AgentID: "B", TenantID: "tenant-b"})

	_ = mA.SendNow(context.Background())
	_ = mB.SendNow(context.Background())

	tenantA := sinkA.last().TenantID
	tenantB := sinkB.last().TenantID

	if tenantA != "tenant-a" {
		t.Errorf("A tenant: want tenant-a, got %q", tenantA)
	}
	if tenantB != "tenant-b" {
		t.Errorf("B tenant: want tenant-b, got %q", tenantB)
	}
	if tenantA == tenantB {
		t.Error("tenants collided across monitors")
	}
}

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
