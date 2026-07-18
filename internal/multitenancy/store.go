// Package multitenancy implements the v18692-4 cross-isolation contract.
//
// The pilot cross-isolation contract says:
//
//   - 50 goroutines × 5 tenants concurrently write/read records.
//   - Each tenant's records MUST NOT bleed into another tenant's view.
//   - Every write emits an audit-log entry carrying tenant_id + job_id.
//   - Cross-tenant access attempts MUST be denied.
//
// In-process implementation (no postgres for v18692-4 pilot). The
// store maps tenant_id -> []*Record; reads/writes go through a
// per-tenant mutex so concurrent access is safe. Cross-tenant reads
// are explicitly denied and audit-logged.
//
// v18693+ will swap this in-process store for the PgDLQStore /
// pgx-backed store per the v18693-3 finalisation story; the
// interface stays the same so the cross-isolation E2E tests
// continue to assert the same contract.
package multitenancy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

// Record is one tenant-scoped data row.
type Record struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	TenantID  string    `json:"tenant_id"`
	JobID     string    `json:"job_id"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	TS       time.Time `json:"ts"`
	Event    string    `json:"event"` // write|read|cross_tenant_attempt
	TenantID string    `json:"tenant_id"`
	JobID    string    `json:"job_id,omitempty"`
	ActorID  string    `json:"actor_id,omitempty"`
	TargetID string    `json:"target_id,omitempty"`
	Result   string    `json:"result"` // ok|denied
	Reason   string    `json:"reason,omitempty"`
}

// Store is a per-tenant keyed store with cross-isolation guarantees.
type Store struct {
	mu      sync.RWMutex
	records map[string]map[string]Record // tenant -> key -> record
	auditMu sync.Mutex
	audit   []AuditEntry
}

// NewStore returns an empty Store with audit log initialised.
func NewStore() *Store {
	return &Store{
		records: map[string]map[string]Record{},
	}
}

// Audit returns a copy of the audit log (test/observability surface).
func (s *Store) Audit() []AuditEntry {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	out := make([]AuditEntry, len(s.audit))
	copy(out, s.audit)
	return out
}

func (s *Store) appendAudit(e AuditEntry) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	s.audit = append(s.audit, e)
}

// ErrCrossTenant is returned when a caller attempts to read or write a
// tenant's records using a context that carries a different tenant_id.
type ErrCrossTenant struct {
	Want string
	Got  string
}

func (e ErrCrossTenant) Error() string {
	return fmt.Sprintf("multitenancy: cross-tenant access denied (context=%q, target=%q)", e.Got, e.Want)
}

// Put writes r into the store. The tenant_id on r MUST equal the
// tenant_id carried in ctx (via tenantid.WithTenantID); otherwise the
// write is denied and audit-logged.
func (s *Store) Put(ctx context.Context, r Record) error {
	if r.TenantID == "" {
		return fmt.Errorf("multitenancy: record missing tenant_id")
	}
	ctxTenant := tenantid.TenantIDFrom(ctx)
	if ctxTenant != r.TenantID {
		s.appendAudit(AuditEntry{
			TS: time.Now().UTC(), Event: "cross_tenant_attempt",
			TenantID: ctxTenant, TargetID: r.TenantID,
			Result: "denied",
			Reason: fmt.Sprintf("ctx=%q record=%q", ctxTenant, r.TenantID),
		})
		return ErrCrossTenant{Want: r.TenantID, Got: ctxTenant}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	if s.records[r.TenantID] == nil {
		s.records[r.TenantID] = map[string]Record{}
	}
	s.records[r.TenantID][r.Key] = r
	s.mu.Unlock()
	s.appendAudit(AuditEntry{
		TS: time.Now().UTC(), Event: "write",
		TenantID: r.TenantID, JobID: r.JobID, Result: "ok",
	})
	return nil
}

// Get reads a single record by tenant+key. Same isolation as Put.
func (s *Store) Get(ctx context.Context, tenant, key string) (Record, error) {
	ctxTenant := tenantid.TenantIDFrom(ctx)
	if ctxTenant != tenant {
		s.appendAudit(AuditEntry{
			TS: time.Now().UTC(), Event: "cross_tenant_attempt",
			TenantID: ctxTenant, TargetID: tenant,
			Result: "denied",
			Reason: "get",
		})
		return Record{}, ErrCrossTenant{Want: tenant, Got: ctxTenant}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.records[tenant]; ok {
		if r, ok := m[key]; ok {
			s.appendAudit(AuditEntry{
				TS: time.Now().UTC(), Event: "read",
				TenantID: tenant, Result: "ok",
			})
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("not found")
}

// List returns all records for the tenant carried in ctx. Cross-tenant
// access denied.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	t := tenantid.TenantIDFrom(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.records[t]
	out := make([]Record, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	s.appendAudit(AuditEntry{
		TS: time.Now().UTC(), Event: "read",
		TenantID: t, Result: "ok",
	})
	return out, nil
}

// Count returns the total number of records for tenant (test helper).
func (s *Store) Count(tenant string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records[tenant])
}
