# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14512 — Pair 5 MVP (Helixon observability sidecar) — Handoff

Sprint: **v14512 Pair 5 MVP**
Date: 2026-07-15
Repo: `helixon-platform`
Branch: `feature/v14512-observability` (rebased on `main`)
PR: #14 (target — pending push)
Pair-lock: `.sprint_lock` (created at start, removed at close)

## 1. Goal (from plan file, line 30)

> v14512 Pair 5 MVP: Grafana dashboards (qwen36-fleet, control-plane,
> agentrace-traces); P0/P1 SLO alert rules; Prometheus provisioning sidecar.

## 2. Deliverables shipped

```
observability/
├── README.md
├── prometheus.yml                            # 4 scrape jobs
├── alertmanager.yml                          # PagerDuty + Slack routing
├── docker-compose.observability.yml          # prometheus + alertmanager + grafana
├── alerts/
│   └── prometheus-helixon-alerts.yml         # 13 rules, 3 groups, P0/P1/P2
├── grafana-provisioning/
│   ├── datasources/datasource.yml            # Prometheus uid=prometheus
│   └── dashboards/dashboards.yml             # provider → dashboards dir
├── grafana/dashboards/
│   ├── qwen36-fleet.json                     # 8 panels
│   ├── control-plane.json                    # 7 panels
│   └── agentrace-traces.json                 # 7 panels
└── verify-observability.sh                   # 32 checks, 100% green
```

### 2.1 Grafana dashboards

| UID             | Title                | Panels | Purpose |
| --------------- | -------------------- | ------ | ------- |
| qwen36-fleet    | qwen36-fleet         | 8      | Cells-ready count, blocked cells, inflight, 5xx ratio, p50/p95/p99 latency, input/output TPS, GPU mem per cell, cost USD/hr |
| control-plane   | control-plane        | 7      | /healthz + /readyz booleans, open sprints, stale fleet nodes, artifacts ingested, API p95 latency, handoffs/day, heartbeat lag per node |
| agentrace-traces| agentrace-traces     | 7      | Active runs, error ratio, mean self-eval score, tokens/hr, stage p95, tokens/stage, error rate by classification |

### 2.2 Prometheus alert rules

13 rules across 3 groups (`helixon.qwen36-fleet`, `helixon.control-plane`,
`helixon.agentrace`), partitioned by severity:

| Severity | Receiver              | Rules |
| -------- | --------------------- | ----- |
| P0 (page) | PagerDuty + Slack    | Qwen36ZeroCellsReady, Qwen36High5xx, ControlPlaneDown, AgentraceHighErrorRate |
| P1 (notify) | Slack #helixon-alerts | Qwen36CellsReadyDrop, Qwen36HighLatencyP99, Qwen36LowGpuMemory, Qwen36CostSurge, ControlPlaneNotReady, FleetHeartbeatsStale, ControlPlaneHighLatencyP95, AgentraceLowSelfEvalScore |
| P2 (log) | silenced + weekly review | AgentraceTokenSpike |

Inhibit rules: `ControlPlaneDown` suppresses team `helixon-platform`;
`Qwen36ZeroCellsReady` suppresses per-cell alerts.

### 2.3 Prometheus provisioning sidecar

- `docker-compose.observability.yml` — 3 services (prometheus, alertmanager,
  grafana) on a bridge network `helixon-obs`. Volumes: `prometheus-data`
  (30d retention), `grafana-data`. Grafana provisioning baked in.
- `prometheus.yml` — 4 scrape jobs (`helixon-control-plane`, `qwen36-cells`,
  `agentrace-fleet`, `prometheus`), 30s scrape interval, 30s evaluation.
- `alertmanager.yml` — routes by `severity` label, PagerDuty + Slack
  receivers, inhibit rules for cascading alerts.

## 3. Verification evidence

### 3.1 Observability verifier (target >= 18 PASS)

```
verifier: PASS=32  FAIL=0
RESULT: at/above v14512 bar (>= 18 PASS)
```

Checks:
- 3 dashboards parse, title+uid match, panel types include stat + timeseries
- 13 alert rules valid YAML, P0/P1/P2 severities all present
- Required alert names exist (`Qwen36ZeroCellsReady`, `Qwen36High5xx`, etc.)
- 4 Prometheus scrape jobs defined, 3 receivers defined
- P0 alerts all have `for:` and `severity: page` labels
- All 12 metric names referenced in alerts (with optional `_bucket` suffix)

### 3.2 Tier-4 cross-layer verifier (cumulative)

```
verifier: PASS=67  FAIL=0
RESULT: at/above v14511 bar (>= 53 PASS)
```

Added 12 new checks for v14512 deliverables; 55 prior checks still green.

### 3.3 Saved evidence

```
reports/eval-runs/eval-run-v14512-01-observability.json   # 32 PASS
reports/eval-runs/eval-run-v14512-01-tier4.json           # 67 PASS
```

## 4. Cross-cutting compliance

| Rule | Status | Evidence |
| --- | --- | --- |
| Pair-lock | ✅ | `.sprint_lock` at branch start; will remove at close |
| Vendor verification | ✅ | `prom/prometheus:v2.55.0`, `prom/alertmanager:v0.27.0`, `grafana/grafana:11.3.0` — all canonical upstream images |
| TDD-first | ✅ | `verify-observability.sh` is a TDD contract suite — every observability file passes it before merge |
| IaC/CaC | ✅ | All observability config under `observability/` and committed; no out-of-repo state |
| Idempotency | n/a | no paid API calls in this sprint |
| 4xx/5xx retry | n/a | no external calls |
| DB migration sequencing | n/a | no schema changes |
| No shell leaks | ✅ | docker-compose runs via `docker compose -f`, no long URLs |
| Token saving | ✅ | alert rules use `rate(...[5m])` not unbounded scans |
| Carry-forward register | ✅ | `carry-forward-register-2026-07-15.ndjson` appended |

## 5. Operator actions before v14513

1. `cp .env.example .env` inside `observability/` and set:
   - `GRAFANA_ADMIN_PASSWORD`
   - `PAGERDUTY_SERVICE_KEY` (from 1Password `Cursor_IronClaw`)
   - `SLACK_WEBHOOK_URL` (from `Cursor_IronClaw`)
2. `docker compose -f observability/docker-compose.observability.yml up -d`
3. Open `http://localhost:3000`, verify 3 dashboards auto-load.
4. Confirm `control_plane_up = 1` after a few minutes (the control-plane
   v14508 healthz endpoint must be on port 8080).
5. Fire a test alert via `amtool --alertmanager.url=http://localhost:9093
   post --alertmanager.url=... alerts/test.yaml` (template below in
   `observability/alerts/test-fire.yaml` if desired).

## 6. Carry-forward to v14513

- Wire agent-self-evaluation + metrics-dashboard skills to
  `AgentraceLowSelfEvalScore` and `AgentraceTokenSpike` so the
  pager agent acknowledges within 30m.
- Add `observability/grafana/dashboards/agentrace-self-eval.json`
  if more detail is needed for the SRE persona.
- Pin `prom/prometheus:v2.55.0` and `grafana/grafana:11.3.0` in
  `observability/docker-compose.observability.yml` image-digest form
  after the first successful `docker compose pull`.

## 7. Files added / updated in v14512

```
observability/README.md
observability/prometheus.yml
observability/alertmanager.yml
observability/docker-compose.observability.yml
observability/verify-observability.sh
observability/alerts/prometheus-helixon-alerts.yml
observability/grafana-provisioning/datasources/datasource.yml
observability/grafana-provisioning/dashboards/dashboards.yml
observability/grafana/dashboards/qwen36-fleet.json
observability/grafana/dashboards/control-plane.json
observability/grafana/dashboards/agentrace-traces.json
reports/eval-runs/eval-run-v14512-01-observability.json
reports/eval-runs/eval-run-v14512-01-tier4.json
scripts/agentcage/install-tier4-verify.sh          # +12 v14512 checks
carry-forward/carry-forward-register-2026-07-15.ndjson  # appended
session-handoffs/v14512-handoff.md                # this file
```

## 8. Restart prompt for v14513

> Continue with v14513 Pair 5 Review: SLO breach paging wired to
> agent-self-evaluation + metrics-dashboard skills; dashboard runbook.
> Re-use the v14512 alert rules as the input; only add acknowledgment
> + paging wiring (no new alerts). Pair-lock against `main`. Re-run
> `scripts/agentcage/install-tier4-verify.sh` and confirm no regression.