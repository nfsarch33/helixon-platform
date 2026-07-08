# ADR 0001: Helixon control-plane persistent schema

- **Status:** Accepted (v14509 Sentrux pair-3 audit, 2026-07-15)
- **Deciders:** Helixon platform owners (nfsarch33)
- **Sprint:** v14509
- **Supersedes:** none
- **Superseded by:** none

## Context

The Helixon control plane (deployed via `charts/helixon-control-plane/`
and running as the `helixon platform` subcommand on the `helixon`
binary) currently has **no persistent storage**. Sprint state, agent
heartbeats, fleet node registry, and artifact metadata are all held in
process memory or pushed out to the A2A gateway / Sprintboard MCP via
HTTP. This ADR commits to a minimal relational schema for the four
core entities so that:

1. Sprint lifecycle events survive a control-plane restart.
2. Fleet node heartbeats can be replayed for incident investigation.
3. Artifacts (bench logs, supply-chain audit results, decision docs)
   can be cross-referenced to the sprint that produced them.
4. Future Grafana dashboards can join across these tables without
   scraping JSON blobs from logs.

The schema is deliberately minimal — v14509 is the first time we
commit to columns, so we get to choose. Follow-up ADRs will cover
sharding, retention, and PII redaction.

## Decision

We adopt SQLite (via `modernc.org/sqlite` — already in go.mod) as the
control-plane storage engine for the single-binary k3s deployment that
the v14506 Helm chart targets. For multi-replica deployments, the
schema is wire-compatible with PostgreSQL via a thin driver swap.

### Schema

```sql
-- Sprint: a paired-sprint cycle (MVP/QA -> Review/Self-improve) or
-- a Sentrux audit sprint. One row per sprint.
CREATE TABLE sprint (
    id              TEXT PRIMARY KEY,         -- 'v14509'
    pair_id         TEXT NOT NULL,            -- 'pair-3' (or 'prereq', 'final')
    kind            TEXT NOT NULL,            -- 'mvp' | 'review' | 'sentrux' | 'prereq' | 'final'
    started_at      TIMESTAMP NOT NULL,
    closed_at       TIMESTAMP,                -- null while in-flight
    release_tag     TEXT,                     -- 'sentrux-2026-07-15'
    pr_numbers      TEXT,                     -- JSON array of merged PR numbers
    handoff_doc     TEXT NOT NULL,            -- path under session-handoffs/
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_sprint_pair ON sprint(pair_id);
CREATE INDEX idx_sprint_kind ON sprint(kind);
CREATE INDEX sprint_closed_at_idx ON sprint(closed_at);

-- Artifact: any file produced by a sprint (handoff doc, evidence log,
-- ADR, supply-chain audit NDJSON, etc). One row per file. Content is
-- referenced by path, not embedded — the actual bytes live on disk so
-- git can audit them.
CREATE TABLE artifact (
    id              TEXT PRIMARY KEY,         -- ULID
    sprint_id       TEXT NOT NULL REFERENCES sprint(id),
    kind            TEXT NOT NULL,            -- 'handoff' | 'evidence' | 'adr' | 'audit' | 'bench' | 'image'
    path            TEXT NOT NULL,            -- relative to repo root
    sha256          TEXT,                     -- optional; populated when file is committed
    byte_size       INTEGER,
    captured_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_artifact_sprint ON artifact(sprint_id);
CREATE INDEX idx_artifact_kind ON artifact(kind);

-- Fleet_node: a single host in the fleet (win1, wsl1, oracle-jump, etc).
CREATE TABLE fleet_node (
    id              TEXT PRIMARY KEY,         -- 'win1', 'wsl1', 'oracle-jump'
    host_kind       TEXT NOT NULL,            -- 'windows' | 'wsl' | 'oracle-jump' | 'oci-jump'
    tailscale_ip    TEXT,
    tailscale_dns   TEXT,
    ssh_user        TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT 1,
    notes           TEXT,                     -- e.g., 'mirror network; no ssh from wsl'
    first_seen_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_fleet_node_active ON fleet_node(is_active);
CREATE UNIQUE INDEX idx_fleet_node_dns ON fleet_node(tailscale_dns) WHERE tailscale_dns IS NOT NULL;

-- Heartbeat: a periodic "I'm alive" report from an agent or a fleet
-- node. The control plane itself emits one per minute; fleet nodes
-- emit one per 5 minutes (configurable). High write volume -> SQLite
-- is fine for single-replica; switch to TimescaleDB / Postgres +
-- BRIN if volume exceeds 10k rows/day.
CREATE TABLE heartbeat (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    source          TEXT NOT NULL,            -- 'control-plane' | 'agent:<id>' | 'node:<id>'
    agent_id        TEXT,                     -- null for control-plane and node heartbeats
    node_id         TEXT,                     -- null for control-plane and agent heartbeats
    phase           TEXT,                     -- agent phase (only set for agent rows)
    iterations      INTEGER,                  -- agent iterations since boot
    tokens_used     INTEGER,                  -- agent tokens used since boot
    uptime_sec      INTEGER NOT NULL,
    extra           TEXT,                     -- JSON blob of metric-specific extras
    received_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_heartbeat_source ON heartbeat(source, received_at DESC);
CREATE INDEX idx_heartbeat_agent ON heartbeat(agent_id, received_at DESC) WHERE agent_id IS NOT NULL;
CREATE INDEX idx_heartbeat_node ON heartbeat(node_id, received_at DESC) WHERE node_id IS NOT NULL;
```

### Migrations

- Migration files live under `internal/helixon/controlplane/migrations/`
  with the format `NNNN_description.up.sql` and `NNNN_description.down.sql`.
- The migration runner is invoked from `cmd/helixon platform` on boot
  before the HTTP listener binds. It is idempotent (skips already-applied
  migrations) and uses `golang-migrate/migrate` semantics.
- A schema version is recorded in a `schema_version` table; the
  control plane refuses to boot if the migration set is older than
  the binary's `MinSupportedSchemaVersion` constant.

### Why SQLite (not Postgres)?

1. The v14506 Helm chart targets k3s single-node deployments where
   running Postgres is overkill.
2. `modernc.org/sqlite` is pure Go (no CGo) and matches our `no shell
   leak` global rule.
3. SQLite WAL mode handles concurrent reads + a single writer; that
   matches the v14506 replica count of 1.
4. The schema is wire-compatible with Postgres; v14512 can swap
   drivers behind a `Storage` interface without a schema migration.

### Why now?

We have carried the v14506 "plan-vs-reality" gap where the control
plane was deployed without persistence. v14509 Sentrux audit is the
natural place to commit the schema because:

- The chart is now on main (PR #7 merged).
- The retry helper (PR #9) handles transient DB errors.
- The op CLI write-path is unblocked (Phase 0.2), so the migration
  runner can be triggered from a 1P-stored script.

## Consequences

### Positive

- Sprint closeouts are atomic: write `sprint.closed_at` + child
  `artifact` rows in a single transaction; no more "handoff doc
  written but main HEAD never tagged" races.
- Grafana can join `heartbeat` x `fleet_node` to show node uptime SLOs.
- Future retention policy can drop `heartbeat` rows older than N days
  without losing sprint/artifact history.

### Negative

- Migrations add a boot dependency: the control plane now needs the
  migrations directory to be writable and `schema_version` to be
  readable.
- SQLite single-writer bottleneck; multi-replica control plane will
  need Postgres + a migration runner that uses advisory locks.

### Risks

- Schema migration during a k8s rolling update could race; mitigation
  is to use `golang-migrate` with `--lock-timeout` and to set the
  Helm chart `strategy.type=Recreate` for the control-plane StatefulSet.
- Heartbeat volume could explode; mitigation is the partial indexes
  above + a per-source rate cap.

## Implementation plan

This ADR commits the schema only. Implementation lives in v14510+:

- v14510: `internal/helixon/controlplane/migrations/0001_initial.up.sql`
  + `down.sql` + a `migrate` CLI subcommand.
- v14511: wire `sprint` and `artifact` writes into the closeout path.
- v14512: Grafana panels consuming `heartbeat` (the v14512 deliverable).
- v14513: alert rules on heartbeat freshness (P0/P1 SLOs).
- v14515: retrospective on whether SQLite is still adequate.

## References

- `internal/helixon/controlplane/heartbeat.go` — heartbeat payload schema (already exists)
- `charts/helixon-control-plane/values.yaml` — chart config (already exists)
- `session-handoffs/v14509-handoff.md` — sprint closeout
- `cursor-global-kb/decisions/driftctl-eol-2026-07-09.md` — sibling ADR-style decision doc