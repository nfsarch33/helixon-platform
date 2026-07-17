// Package tenantid centralises the per-tenant context propagation used
// across the Helixon platform. Tenants are identified by a short string
// (UUID, slug, or email) and are propagated via:
//
//   - Environment variable `HELIXON_TENANT_ID` (canonical, read at boot)
//   - context.Context (per-request, per-job) via WithTenantID / TenantIDFrom
//   - Direct field on structs (Email.TenantID, ToolResult.TenantID, etc.)
//
// The package exposes a single Source-of-truth: NewResolver() reads the
// env var once and returns a Resolver whose TenantID method returns the
// resolved value for any goroutine, falling back to "default" if unset.
//
// Why centralise? Per CF-172, every internal service (notify, secrets,
// toolresult, svcregistry) needs to attribute cost and audit events to a
// tenant. Without a single resolver, every package re-implements env
// reading and risks drift (one reads `HELIXON_TENANT_ID`, another reads
// `TENANT_ID`, another reads from a config struct, etc.).
package tenantid

import (
	"context"
	"os"
	"testing"
)

func TestEnvTenantID_DefaultsToDefault(t *testing.T) {
	// Unset env var to test default fallback.
	t.Setenv("HELIXON_TENANT_ID", "")
	got := EnvTenantID()
	if got != "default" {
		t.Fatalf("expected fallback to %q, got %q", "default", got)
	}
}

func TestEnvTenantID_ReadsEnv(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "acme-corp")
	got := EnvTenantID()
	if got != "acme-corp" {
		t.Fatalf("expected %q, got %q", "acme-corp", got)
	}
}

func TestEnvTenantID_TrimsWhitespace(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "  acme-corp  ")
	got := EnvTenantID()
	if got != "acme-corp" {
		t.Fatalf("expected trimmed %q, got %q", "acme-corp", got)
	}
}

func TestContext_WithTenantID(t *testing.T) {
	ctx := WithTenantID(context.Background(), "tenant-a")
	got := TenantIDFrom(ctx)
	if got != "tenant-a" {
		t.Fatalf("expected %q, got %q", "tenant-a", got)
	}
}

func TestContext_TenantIDFrom_DefaultsToDefault(t *testing.T) {
	got := TenantIDFrom(context.Background())
	if got != "default" {
		t.Fatalf("expected fallback to %q, got %q", "default", got)
	}
}

func TestResolver_FromEnv(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "tenant-resolver")
	r := NewResolver()
	if r.TenantID() != "tenant-resolver" {
		t.Fatalf("expected %q, got %q", "tenant-resolver", r.TenantID())
	}
}

func TestResolver_FromContextOverridesEnv(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "env-tenant")
	r := NewResolver()
	ctx := WithTenantID(context.Background(), "ctx-tenant")
	if got := r.TenantIDFromContext(ctx); got != "ctx-tenant" {
		t.Fatalf("expected ctx-tenant to win over env, got %q", got)
	}
}

func TestResolver_DefaultFallbackWhenEmpty(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "")
	r := NewResolver()
	if r.TenantID() != "default" {
		t.Fatalf("expected default fallback, got %q", r.TenantID())
	}
}

func TestResolver_ConcurrentSafe(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "concurrent-tenant")
	r := NewResolver()
	const n = 100
	done := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			done <- r.TenantID()
		}()
	}
	for i := 0; i < n; i++ {
		got := <-done
		if got != "concurrent-tenant" {
			t.Fatalf("expected %q, got %q", "concurrent-tenant", got)
		}
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
