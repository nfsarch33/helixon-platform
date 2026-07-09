# v14540 — Service registry schema + registra Go binary + ingestion (Pair 1 MVP)

**Status:** ✅ Completed
**Date:** 2026-07-09
**Sprint:** v14540 (Pair 1 MVP)
**Pair-locked at:** 2026-07-09T14:35:00+10:00

## Goal
Unify the 6 fragmented Helixon-fleet sources of truth into a single canonical,
machine-readable **service registry** and ship a Go CLI (`registra`) that loads,
queries, and health-probes the registry.

## Deliverables

### 1. Canonical registry SOT
- `inventory/services/registry.yaml` — 25.9 kB, schema_version=1
- `inventory/services/registry.json` — 35.2 kB, machine-readable mirror
- 16 services | 14 nodes | 4 LLM cells | 82 1Password credentials

Ingested source files:
1. `inventory/fleet/nodes.yaml`            (fleet nodes)
2. `scripts/fleet/qwen36-matrix.yaml`      (LLM cells)
3. `configs/llm-cluster-router.yml`        (router ports, route rules)
4. `inventory/1password/items.csv`         (vault item index)
5. `sop/install-tier1-toolchain.md`        (binary paths, k3s port)
6. `sop/install-tier2-binaries.md`         (binary PATH order)
Plus synthesised services from `sop/engram-install-wsl1.md`,
`sop/sprintboard-install.md`, and observability configs from v14538.

Fixed `inventory/fleet/nodes.yaml` retired_hosts indentation so it parses
cleanly with strict YAML loaders (`- macbook` → `- alias: macbook`).

### 2. registra Go package + CLI
- `internal/registra/registra.go`           (loader + query API)
- `internal/registra/registra_test.go`      (6 unit tests, all PASS)
- `cmd/registra/main.go`                    (Cobra-free CLI, tabwriter output)
- Built binary: `~/local/bin/registra` (9.0 MB)

### Subcommands
- `registra summary` — version + counts
- `registra list [--node ALIAS] [--kind KIND]`
- `registra show NAME`
- `registra nodes`
- `registra cells`
- `registra credential TITLE`
- `registra health [--node ALIAS]` — probes `/health` endpoints over HTTP
- Global `--registry PATH` flag (default `inventory/services/registry.yaml`)

### Tests
```
=== RUN   TestRegistryLoadAndList       --- PASS
=== RUN   TestRegistryFindByName        --- PASS
=== RUN   TestRegistryFilterByNode      --- PASS
=== RUN   TestRegistryFilterByKind      --- PASS
=== RUN   TestRegistryFindCredentialByTitle --- PASS
=== RUN   TestRegistryFindNodeByAlias   --- PASS
ok  github.com/nfsarch33/helixon-platform/internal/registra
```

## Smoke evidence

```
$ registra summary
helixon service registry
  registry_version : 0.1.0-v14540
  schema_version   : 1
  central_node     : wsl1
  services         : 16
  nodes            : 14
  llm_cells        : 4
  credentials      : 82
  source_files     : 6
```

## Carry-forwards to v14541
- (none blocking)

## Cross-references
- `inventory/services/registry.yaml` — canonical SOT
- `internal/registra/` — Go API for downstream consumers
- `cmd/registra/` — CLI binary at `~/local/bin/registra`