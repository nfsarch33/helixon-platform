package svcregistry

import (
	"context"

	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

// RegisterWithTenant is the v18675-3 (CF-172) entry point for service
// registration. It resolves the tenant id from the request context
// (preferred) or the boot-time env var, stamps it onto the Service,
// and delegates to Registry.Register.
//
// Single-tenant callers that pre-populate `s.TenantID` directly should
// keep using Register(); this helper exists to make the per-request
// tenant attribution explicit and uniform across the platform.
func (r *Registry) RegisterWithTenant(ctx context.Context, s Service) error {
	if s.TenantID == "" {
		s.TenantID = tenantid.TenantIDFrom(ctx)
		if s.TenantID == tenantid.DefaultTenantID {
			s.TenantID = tenantid.EnvTenantID()
		}
	}
	return r.Register(s)
}
