# runx-public-repo-gate: allow-file internal_service_id
# observability/

Helixon observability sidecar (Prometheus + Grafana + Alertmanager).

Authored 2026-07-15 as part of **v14512 Pair 5 MVP**.

## Layout

```
observability/
├── prometheus.yml                                 # scrape config
├── alerts/
│   └── prometheus-helixon-alerts.yml              # P0/P1/P2 alert rules
├── alertmanager.yml                               # PagerDuty + Slack routing
├── docker-compose.observability.yml               # sidecar compose
├── grafana-provisioning/
│   ├── datasources/datasource.yml                 # Prometheus uid=prometheus
│   └── dashboards/dashboards.yml                  # provider → /var/lib/grafana/dashboards
└── grafana/dashboards/
    ├── qwen36-fleet.json                          # cells up/down, latency, GPU mem, cost
    ├── control-plane.json                         # /healthz, /readyz, fleet heartbeats, sprints
    └── agentrace-traces.json                      # stages, tokens, errors, self-eval score
```

## Bring-up (operator action)

```bash
cd observability/
cp .env.example .env  # then set GRAFANA_ADMIN_PASSWORD, PAGERDUTY_SERVICE_KEY, SLACK_WEBHOOK_URL
docker compose -f docker-compose.observability.yml up -d
# Grafana:    http://localhost:3000  (admin / $GRAFANA_ADMIN_PASSWORD)
# Prometheus: http://localhost:9090
# Alerts UI:  http://localhost:9093
```

Dashboards are auto-loaded from `grafana/dashboards/` via the file
provisioner. If you add a new dashboard, drop it in that folder and
Grafana picks it up within 30s.

## Alert severities

| Severity | Receiver              | Page? | Cadence                |
| -------- | --------------------- | ----- | ---------------------- |
| `page`   | PagerDuty             | yes   | repeat every 1h        |
| `notify` | Slack #helixon-alerts | no    | group every 30s        |
| `log`    | (silenced)            | no    | weekly review          |

## Verify

```bash
bash observability/verify-observability.sh
```

Target: PASS >= 18. Exit code 0 at/above bar.

## Prometheus metrics referenced

The alert rules assume these metrics are scraped from the corresponding
services. If you change a metric name, change both the alert rule and
the producer in lockstep.

| Metric | Source |
| ------ | ------ |
| `qwen36_cell_ready` | qwen36-matrix.yaml cell status → exporter |
| `qwen36_request_total{status}` | qwen36 cell /metrics |
| `qwen36_request_latency_seconds` | histogram on every LLM call |
| `qwen36_gpu_mem_free_mib{cell_id}` | nvidia-smi / dcgmi exporter |
| `qwen36_est_cost_usd_total` | internal/costobs (v14511) → Prometheus pushgateway |
| `control_plane_up` | /healthz of helixon control plane |
| `control_plane_ready` | /readyz of helixon control plane |
| `control_plane_fleet_node_last_seen_seconds{node}` | heartbeat table |
| `control_plane_http_request_duration_seconds{endpoint}` | API middleware |
| `control_plane_sprints_open` | sprint table count |
| `control_plane_sprint_handoff_total` | counter on closeout |
| `control_plane_artifacts_total` | counter on artifact ingest |
| `agentrace_stage_total{stage,status,classification}` | agentrace SDK |
| `agentrace_stage_duration_seconds` | agentrace SDK histogram |
| `agentrace_self_eval_score` | agentrace self-eval |
| `agentrace_tokens_total{stage}` | agentrace SDK counter |