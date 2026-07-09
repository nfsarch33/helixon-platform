# v14578 — Service registry dedupe + fleet-registrar schema migration

## Sprint goal

Per `v14576-v14593_deferred_cf_+_production-ready_27dc4bb3.plan.md` Pair 2 MVP, perform a
dedupe audit of the 19 live `svcregistryd` entries vs the 16-entry SOT, and migrate the
`fleet-registrar` entry into the canonical schema (post-ADR-0097).

## Dedupe audit (pre-migration)

| Layer                    | Count | Format             |
|--------------------------|-------|--------------------|
| `inventory/services/registry.json` (SOT, JSON) | 16 | canonical fields (`name`/`kind`/`primary_node`/`address`/`port`/`health_path`) |
| `inventory/services/registry.yaml` (SOT, YAML) | 16 | identical to JSON |
| `svcregistryd` live      | 19    | runtime (`name`/`host`/`port`/`protocol`/`owner`/`status`/`tailscale_ip`) |
| `fleet-registrar` SQLite | 0     | empty (services table) |
| `fleet-registrar` registry.json | 11 (services) + 3 (hosts) | legacy schema (`type`/`node`/`deploy_type`) |

### Live ∩ SOT analysis
- **In both**: 16/16 (perfect alignment)
- **In SOT only**: 0
- **In live only (runtime)**: 3 — `fleet-agent-wsl1`, `fleet-registrar`, `smoke-v14561-1783599808`

### Deprecated `kind: register` scan
Searched both SOT files for the deprecated `kind: register` value. **0 matches**.
The v14576 sprint spec mentioned a "deprecated `kind: register`" migration for fleet-registrar;
in practice the SOT never had such an entry — fleet-registrar was only in the live registry
and SQLite. So the migration path is:
1. Add a canonical-schema `fleet-registrar` entry to BOTH SOT files (json + yaml)
2. Update the legacy `registry.json` hosts table to fix the stale wsl1 tailscale IP

## Migrations performed

### 1. SOT (registry.json + registry.yaml)
Added `fleet-registrar` to both SOT files:

```yaml
- name: fleet-registrar
  kind: service-registry
  primary_node: wsl1
  address: 127.0.0.1
  port: 9103
  health_path: /health
  binary: ~/.local/bin/fleet-registrar
  owner_sprint: v14578
  source: evidence/v14578-registry-dedupe/README.md
  tags: [registry, wsl1, migrated-from-wsl2]
  status: registered
  registered_at: 2026-07-10T03:18:00+10:00
  notes: Migrated from wsl2 to wsl1 in v14572. Schema aligned to ADR-0097 in v14578.
```

SOT count: 16 → **17** (both files).

### 2. fleet-registrar registry.json — stale wsl1 host entry
PRE-migration:
```json
{"gpu":"","name":"wsl1","role":"management-offline","status":"offline","tailscale_ip":"100.119.90.30"}
```
POST-migration:
```json
{"gpu":"RTX-3090:2 + RTX-2070:1","name":"wsl1","role":"central-server","status":"up","tailscale_ip":"100.84.108.92"}
```

The stale `100.119.90.30` IP was from before wsl1 was reinstalled at `100.84.108.92` in v14540.
The `offline` status was because the registrar hadn't seen wsl1 heartbeat since the move.

### 3. Bridge re-run (svcregistry-bridge)
```
/home/jaslian/local/bin/svcregistry-bridge -api http://127.0.0.1:7777 \
  -owner v14578 -registry .../inventory/services/registry.yaml
→ registered=17 failed=0 skipped=0
```

POST-bridge live svcregistryd:
- 19 entries: 17 from SOT bridge + 2 runtime (`fleet-agent-wsl1`, `smoke-v14561-...`)
- `fleet-registrar` now in BOTH SOT and live

## Files

- `live-services-baseline.json` — 19 live entries before bridge
- `live-services-post-bridge.json` — 19 live entries after bridge (now includes fleet-registrar from SOT)
- `bridge-run.txt` — bridge stdout (17 `registered` lines + final tally)
- `registrar-schema.txt` — fleet-registrar SQLite schema
- `registrar-registry-baseline.json` — legacy registry.json snapshot (pre-migration)
- `registry.json.registrar-post-migration` — updated fleet-registrar registry.json
- `registry.json.sot-baseline` / `registry.json.sot-pre-migration` / `registry.json.sot-post-migration` — SOT snapshots
- `registry.yaml.sot-pre-migration` / `registry.yaml.sot-post-migration` — YAML SOT snapshots

## Verification

- [x] Live svcregistryd: 19/19 have all canonical fields
- [x] SOT (json + yaml): 17/17 with canonical fields; no deprecated `kind: register`
- [x] Bridge runs idempotently (registered=17, failed=0, skipped=0)
- [x] fleet-registrar migrated from wsl2 → wsl1 schema; stale IP corrected
- [x] Both SOT files synchronized (registry.json == registry.yaml)

## Followups (carry-forward)

- **CF-v14578-01** (new): fleet-registrar SQLite `services` table is empty (count=0). The
  live data lives in `registry.json` only. The bridge from v14560 didn't populate it. If
  fleet-registrar is to be authoritative for service discovery on wsl1, the schema migration
  from `registry.json` (legacy) → SQLite (canonical) needs to be authored. Out of scope for
  v14578; deferred to a future sprint.
- **smoke-v14561-1783599808** is stale test data from v14561 — can be deleted from live
  registry in a future cleanup sprint.