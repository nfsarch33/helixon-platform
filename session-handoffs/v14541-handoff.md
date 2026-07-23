# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14541 — Cursor helix-dev-tools fleet services CLI + service discovery + agent-browser registration UI (Pair 1 Review)

**Status:** ✅ Completed
**Date:** 2026-07-09
**Sprint:** v14541 (Pair 1 Review)

## Goal
Build the user-facing layer on top of v14540's canonical registry:
1. Wire `services` subcommand into `helix-dev-tools` (single-binary, Cursor-aware)
2. Ship `service-discovery` (per-service URL + credentials + node lookup)
3. Ship `svcregistry-bridge` to push registry.yaml into the runtime `svcregistryd`
4. Ship the agent-browser registration UI (`register.html`)

## Deliverables

### 1. helix-dev-tools `services` subcommand
- `internal/services/registry.go` — vendored registra package (renamed `package services`)
- `internal/cli/services_cmd.go` — Cobra command + 7 subcommands
- `internal/cli/services_cmd_test.go` — 3 TDD tests (all PASS)

Subcommands:
- `services summary`
- `services list [--node=ALIAS] [--kind=KIND]`
- `services show NAME`
- `services nodes`
- `services cells`
- `services credential TITLE` (multi-word title supported)
- `services health [--node=ALIAS]` — live HTTP probes
- `services discover NAME` — URL, health, binary, node, suggested creds

Global flag: `--registry PATH` (default = canonical `inventory/services/registry.yaml`)

### 2. svcregistry-bridge Go binary
- `cmd/svcregistry-bridge/main.go` (9.4 MB built)
- `cmd/svcregistry-bridge/main_test.go` — 1 TDD test (PASS, uses httptest)
- Reads `registry.yaml` → POSTs each service to svcregistryd `/api/v1/services`
- Adds tailscale_ip from primary_node lookup
- Flags: `--registry`, `--api`, `--owner`, `--dry-run`, `--timeout`

### 3. Agent-browser registration UI
- `inventory/services/ui/register.html` (5.8 kB)
- Pure HTML+JS, no framework
- Live `GET /api/v1/services` table + `POST` form
- Status feedback inline

## Smoke evidence

```
=== /healthz ===
ok
=== /api/v1/services count ===
services=15
  alertmanager                   0.0.0.0     :9093   http  ts=100.84.108.92
  grafana                        0.0.0.0     :3000   http  ts=100.84.108.92
  k3s-server                     0.0.0.0     :6443   http  ts=100.84.108.92
  llama-server-c2-q4             0.0.0.0     :8011   http  ts=100.84.108.92
  llama-server-c7-q8             0.0.0.0     :8010   http  ts=100.84.108.92
  llm-cluster-router             0.0.0.0     :8787   http  ts=100.84.108.92
  ...
=== /metrics ===
svcregistry_operations_total{op="list",status="ok"} 6
svcregistry_operations_total{op="register",status="ok"} 15
```

`helix-dev-tools services discover engramd` returns:
```
# Service discovery for engramd
URL      : http://127.0.0.1:8280
Health   : http://127.0.0.1:8280/healthz
Binary   : ~/local/bin/engramd
Sprint   : v14532
Source   : sop/engram-install-wsl1.md
Node     : wsl1 (desktop-12ro1af-wsl1 @ 100.84.108.92)
Creds    :
  - mem0-oss-admin-key (op_uri=op://Cursor_IronClaw/mem0-oss-admin-key)
  - mem0-oss-jwt-secret (op_uri=op://Cursor_IronClaw/mem0-oss-jwt-secret)
```

`helix-dev-tools services health --node wsl1` (live HTTP probes):
- engramd :8280 — PASS 200
- grafana :3000 — PASS 200
- llama-server-c7-q8 :8010 — PASS 200
- llm-cluster-router :8787 — PASS 200
- ollama-embed-c6 :11434 — PASS 200
- prometheus :9090 — PASS 200

## Test results

```
=== RUN   TestServicesCmd_Exists          --- PASS
=== RUN   TestServicesList_ParsesYAMLRegistry  --- PASS
=== RUN   TestServicesFindNode            --- PASS
=== RUN   TestBridgeToRegistryAPI         --- PASS
```

## Carry-forwards
- None blocking; this closes the registry loop.

## Cross-references
- `inventory/services/registry.yaml` — canonical SOT (v14540)
- `inventory/services/ui/register.html` — agent-browser UI
- `internal/services/registry.go` — registra package vendored
- `internal/cli/services_cmd.go` — Cobra wiring
- `cmd/svcregistry-bridge/` — bridge binary
- `~/local/bin/helix-dev-tools`, `~/local/bin/svcregistry-bridge`