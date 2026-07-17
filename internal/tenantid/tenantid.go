// Package tenantid centralises per-tenant context propagation across
// the Helixon platform. See tenantid_test.go for the contract.
package tenantid

import (
	"context"
	"os"
	"strings"
	"sync"
)

// DefaultTenantID is the fallback value used when neither the
// `HELIXON_TENANT_ID` env var nor the context-derived tenant ID is set.
// Internal services treat this as a single-tenant / pre-multitenancy
// deployment and bill the cost to the operator.
const DefaultTenantID = "default"

// EnvVar is the canonical environment variable name for tenant
// resolution at boot. Per CF-172, this is the only tenant-id env var
// any internal service should read.
const EnvVar = "HELIXON_TENANT_ID"

// ctxKey is unexported to prevent context-value collisions across
// packages; only this package can construct or extract the tenant id
// from a context.
type ctxKey struct{}

// WithTenantID returns a copy of parent carrying the given tenant id.
// Pass the returned context to internal services (notify, secrets,
// toolresult, svcregistry) so the per-tenant value travels with the
// job/audit trail.
func WithTenantID(parent context.Context, tenantID string) context.Context {
	if tenantID == "" {
		return parent
	}
	return context.WithValue(parent, ctxKey{}, tenantID)
}

// TenantIDFrom extracts the tenant id from ctx, falling back to
// DefaultTenantID when absent. Use this in any function that needs to
// attribute cost / audit / notifications to a tenant.
func TenantIDFrom(ctx context.Context) string {
	if ctx != nil {
		if v, ok := ctx.Value(ctxKey{}).(string); ok && v != "" {
			return v
		}
	}
	return DefaultTenantID
}

// EnvTenantID reads the env var directly. Most callers should use a
// Resolver or TenantIDFrom(ctx); this helper exists for boot-time
// single-shot reads where a context is not yet available.
func EnvTenantID() string {
	return normaliseTenantID(os.Getenv(EnvVar))
}

// Resolver is the boot-time tenant resolver used by long-running
// processes (helixon serve, helixon platform). It reads the env var
// once at construction and is safe for concurrent use.
//
// The resolver is intentionally simple: no refresh on SIGHUP, no
// config-file fallback. The HELIXON_TENANT_ID env var is the source of
// truth for the lifetime of the process.
type Resolver struct {
	once     sync.Once
	tenantID string
}

// NewResolver returns a Resolver seeded with the current value of
// HELIXON_TENANT_ID (or DefaultTenantID when unset). Subsequent env
// changes do NOT update the resolver — restart the process to pick up
// a new tenant.
func NewResolver() *Resolver {
	r := &Resolver{}
	r.once.Do(func() {
		r.tenantID = EnvTenantID()
	})
	return r
}

// TenantID returns the boot-time tenant id.
func (r *Resolver) TenantID() string {
	if r == nil {
		return DefaultTenantID
	}
	r.once.Do(func() {
		r.tenantID = EnvTenantID()
	})
	return r.tenantID
}

// TenantIDFromContext returns the per-context tenant id (preferred for
// per-request / per-job work) falling back to the boot-time resolver
// value, and finally to DefaultTenantID.
func (r *Resolver) TenantIDFromContext(ctx context.Context) string {
	if v := TenantIDFrom(ctx); v != DefaultTenantID {
		return v
	}
	return r.TenantID()
}

// normaliseTenantID trims whitespace and returns DefaultTenantID when
// the input is empty after trimming.
func normaliseTenantID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultTenantID
	}
	return s
}
