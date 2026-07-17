package svcregistry_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/svcregistry"
	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

func newTestRegistry(t *testing.T) *svcregistry.Registry {
	t.Helper()
	dir := t.TempDir()
	return svcregistry.New(filepath.Join(dir, "registry.json"))
}

func mkSvc(name string, port int, tenant string) svcregistry.Service {
	return svcregistry.Service{
		Name:     name,
		Host:     "127.0.0.1",
		Port:     port,
		Protocol: "http",
		Owner:    "v18675-3",
		Status:   "unknown",
		TenantID: tenant,
	}
}

func TestRegisterWithTenant_PropagatesTenantFromContext(t *testing.T) {
	r := newTestRegistry(t)
	ctx := tenantid.WithTenantID(context.Background(), "ctx-tenant")

	if err := r.RegisterWithTenant(ctx, mkSvc("test-svc", 9101, "")); err != nil {
		t.Fatalf("RegisterWithTenant: %v", err)
	}
	got, ok := r.Get("127.0.0.1", "test-svc")
	if !ok {
		t.Fatalf("service not registered")
	}
	if got.TenantID != "ctx-tenant" {
		t.Fatalf("TenantID = %q; want %q", got.TenantID, "ctx-tenant")
	}
}

func TestRegisterWithTenant_FallsBackToEnvWhenNoContext(t *testing.T) {
	r := newTestRegistry(t)
	t.Setenv("HELIXON_TENANT_ID", "env-tenant")

	if err := r.RegisterWithTenant(context.Background(), mkSvc("test-svc", 9102, "")); err != nil {
		t.Fatalf("RegisterWithTenant: %v", err)
	}
	got, _ := r.Get("127.0.0.1", "test-svc")
	if got.TenantID != "env-tenant" {
		t.Fatalf("TenantID = %q; want %q", got.TenantID, "env-tenant")
	}
}

func TestRegisterWithTenant_DefaultsToDefaultWhenNoTenant(t *testing.T) {
	r := newTestRegistry(t)
	t.Setenv("HELIXON_TENANT_ID", "")

	if err := r.RegisterWithTenant(context.Background(), mkSvc("test-svc", 9103, "")); err != nil {
		t.Fatalf("RegisterWithTenant: %v", err)
	}
	got, _ := r.Get("127.0.0.1", "test-svc")
	if got.TenantID != "default" {
		t.Fatalf("TenantID = %q; want %q", got.TenantID, "default")
	}
}

func TestRegisterWithTenant_PreservesExplicitTenant(t *testing.T) {
	r := newTestRegistry(t)
	ctx := tenantid.WithTenantID(context.Background(), "ctx-tenant")

	// Explicit TenantID on the struct takes precedence over the context.
	if err := r.RegisterWithTenant(ctx, mkSvc("test-svc", 9104, "explicit-tenant")); err != nil {
		t.Fatalf("RegisterWithTenant: %v", err)
	}
	got, _ := r.Get("127.0.0.1", "test-svc")
	if got.TenantID != "explicit-tenant" {
		t.Fatalf("TenantID = %q; want %q", got.TenantID, "explicit-tenant")
	}
}

func TestRegisterWithTenant_RoundTripJSON(t *testing.T) {
	r := newTestRegistry(t)
	ctx := tenantid.WithTenantID(context.Background(), "json-tenant")

	if err := r.RegisterWithTenant(ctx, mkSvc("test-svc", 9105, "")); err != nil {
		t.Fatalf("RegisterWithTenant: %v", err)
	}
	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	r2 := svcregistry.New(filepath.Dir(r.Path()) + "/registry.json")
	if err := r2.Load(); err != nil {
		t.Fatalf("Load r2: %v", err)
	}
	got, ok := r2.Get("127.0.0.1", "test-svc")
	if !ok {
		t.Fatalf("service not persisted across reload")
	}
	if got.TenantID != "json-tenant" {
		t.Fatalf("TenantID after reload = %q; want %q", got.TenantID, "json-tenant")
	}
}
