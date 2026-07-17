// Package secretaudit wraps 1Password secret resolution with per-tenant
// audit logging. v18675-3 (CF-172): every secret read is attributed to
// the tenant that triggered it, enabling per-tenant security audits.
//
// The package is intentionally tiny: callers wrap their existing
// onepassword.Client calls in WithAudit() and pass a context seeded
// with `tenantid.WithTenantID(ctx, "...")`. When the context tenant is
// not set, the helper falls back to the boot-time env var
// `HELIXON_TENANT_ID`.
package secretaudit

import (
	"context"
	"log/slog"

	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

// Resolver is the minimal surface of internal/secrets/onepassword that
// secretaudit needs. Production callers pass *onepassword.Client; tests
// pass a stub implementing the same surface.
type Resolver interface {
	ResolveSecret(ctx context.Context, vault, itemUUID, fieldID string) (string, error)
}

// Logger emits one structured NDJSON line per secret read, attributed
// to a tenant. Default (nil) suppresses logging; production should wire
// `slog.Default()` or a per-component logger.
var Logger func(tenantID, vault, itemUUID, fieldID string)

// WithAudit wraps a Resolver call so that the tenant id (from context
// or env) is recorded before the secret is fetched. The returned value
// is the secret's plaintext; errors propagate unchanged.
//
// Use this in any code path that resolves secrets on behalf of a
// tenant (Helixon agent runtime, helixon-platform webhooks, etc.).
func WithAudit(ctx context.Context, r Resolver, vault, itemUUID, fieldID string) (string, error) {
	tenantID := tenantid.TenantIDFrom(ctx)
	if tenantID == tenantid.DefaultTenantID {
		tenantID = tenantid.EnvTenantID()
	}
	if Logger != nil {
		Logger(tenantID, vault, itemUUID, fieldID)
	}
	slog.Debug("secretaudit: resolving secret",
		"tenant_id", tenantID,
		"vault", vault,
		"item_uuid", itemUUID,
		"field_id", fieldID,
	)
	return r.ResolveSecret(ctx, vault, itemUUID, fieldID)
}
