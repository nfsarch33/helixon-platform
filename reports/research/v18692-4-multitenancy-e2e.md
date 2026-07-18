# v18692-4 Multitenancy Cross-Isolation E2E — Evidence Report

**Sprint:** v18692 (Helixon Pilot E2E)
**Story:** v18692-4 multitenancy cross-isolation E2E
**Branch:** `qa/v18692-4-multitenancy-cross-isolation`
**Started:** 2026-07-18T23:48+10:00
**Author/Machine-Id:** cursor-parent@win3-wsl3

## Scope

Per the v18692 plan, v18692-4 ships the multitenancy cross-isolation
E2E contract:

- 50 goroutines × 5 tenants concurrent pilot harness.
- Each tenant's records MUST NOT bleed into another tenant's view.
- Audit log entry per write with `tenant_id + job_id`.
- Cross-tenant access denied on Put / Get / List.

## What Shipped

### `internal/multitenancy/store.go` (new, 200 LOC)

- `Store` — per-tenant keyed in-memory map (`tenant -> key -> Record`).
- `Record` — `{Key, Value, TenantID, JobID, CreatedAt}`.
- `AuditEntry` — `{TS, Event, TenantID, JobID, ActorID, TargetID, Result, Reason}`.
- `Put(ctx, Record)` — write; tenant_id from `ctx` MUST equal `Record.TenantID` else `ErrCrossTenant`.
- `Get(ctx, tenant, key)` — read; same isolation rule.
- `List(ctx)` — list all records for the tenant carried in `ctx`.
- `Count(tenant)` — test helper.
- `Audit()` — read-only copy of the audit log.
- `ErrCrossTenant{Want, Got}` — sentinel error type for denied access.

### `internal/multitenancy/cross_isolation_e2e_test.go` (new, 175 LOC)

- `TestCrossIsolation_50x5_Concurrent` — 50 goroutines × 5 tenants × 10 writes
  = 500 writes. Each worker also attempts one cross-tenant Get. Asserts
  per-tenant count, no foreign record visible in List, audit log shape.
- `TestCrossIsolation_PutForeignCtxDenied` — Put with `tenant_id=X` using
  context carrying `tenant_id=Y` → `ErrCrossTenant`.
- `TestCrossIsolation_GetForeignDenied` — Get with foreign tenant arg.
- `TestCrossIsolation_AuditJobID` — verifies each audit entry's `tenant_id`
  and `job_id` pair.

## Test Results

```
=== RUN   TestCrossIsolation_50x5_Concurrent
    cross_isolation_e2e_test.go:120: 50x5 concurrent: writes=500 reads=5 cross_tenant=50
--- PASS: TestCrossIsolation_50x5_Concurrent (0.00s)
=== RUN   TestCrossIsolation_PutForeignCtxDenied
--- PASS: TestCrossIsolation_PutForeignCtxDenied (0.00s)
=== RUN   TestCrossIsolation_GetForeignDenied
--- PASS: TestCrossIsolation_GetForeignDenied (0.00s)
=== RUN   TestCrossIsolation_AuditJobID
--- PASS: TestCrossIsolation_AuditJobID (0.00s)
PASS
ok  github.com/nfsarch33/helixon-platform/internal/multitenancy  0.003s
```

### Race detection

```
$ go test -race ./internal/multitenancy/... -count=1
ok  github.com/nfsarch33/helixon-platform/internal/multitenancy  1.013s
```

Race-clean.

## Decision Tree Outcomes

| Axis | Outcome | Notes |
|---|---|---|
| 50-goroutine × 5-tenant concurrent | **GREEN** | 500 writes, 100 per tenant, 50 cross-tenant attempts |
| No tenant cross-bleed on List | **GREEN** | Each tenant's List returns only its own records |
| Cross-tenant Put denied | **GREEN** | `ErrCrossTenant` returned, audit-logged |
| Cross-tenant Get denied | **GREEN** | `ErrCrossTenant` returned, audit-logged |
| Audit log carries tenant_id + job_id | **GREEN** | Verified via `TestCrossIsolation_AuditJobID` |
| Race-clean | **GREEN** | `go test -race` passes |

## Files Changed

- `internal/multitenancy/store.go` (new, 200 LOC)
- `internal/multitenancy/cross_isolation_e2e_test.go` (new, 175 LOC)

## Carry-Forward (CF)

| ID | Item | Severity |
|---|---|---|
| v18692-4-cf-001 | Swap in-process Store for PgDLQStore / pgx-backed store (v18693-3 finalisation). Interface stays the same so the cross-isolation E2E continues to assert the contract. | medium |
| v18692-4-cf-002 | Emit audit entries to costobs NDJSON so cross-tenant attempts surface in the operator dashboard. | medium |
| v18692-4-cf-003 | Add `Drop(tenant)` admin op (carefully audited; bulk-delete must require operator approval per `fleet-destructive-op-deny.mdc`). | low |

## Lessons / Anti-Patterns

- **Anti-pattern (avoided):** Building a full postgres-backed store for
  the pilot E2E. The v18692-4 pattern ships an in-process store with
  the same interface the v18693-3 PgDLQStore will satisfy, so the
  contract test is reusable without re-platforming.
- **Anti-pattern (avoided):** Using `t.Errorf` after `t.Fatalf` in a
  goroutine. The v18692-4 pattern keeps the per-tenant assertion in
  the main test goroutine so failure messages surface cleanly.
- **Anti-pattern (avoided):** Trusting ctx-carried tenant_id without
  re-checking against the record's tenant_id. Both must match; the
  store asserts and audit-logs the mismatch.

## References

- Plan: `/home/jason/.cursor/plans/helixon_v18691-v18694_pilot%2Bqa%2Breality%2Bworkspace_e406210d.plan.md`
- Existing tenantid package: `internal/tenantid/tenantid.go`.
- v18692-1 report: `reports/research/v18692-1-live-llm-e2e.md`.
- v18692-2 report: `reports/research/v18692-2-idempotency-e2e.md`.
- v18692-3 report: `reports/research/v18692-3-chatpanel-ui-e2e.md`.
- Branch: `qa/v18692-4-multitenancy-cross-isolation`.

Machine-Id: win3-wsl3