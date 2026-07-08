---
name: metrics-dashboard
description: "Generate a daily/weekly Helixon performance report by consolidating self-eval scores, error rates, fleet health, and cost from the Grafana dashboards (qwen36-fleet, control-plane, agentrace-traces) and Alertmanager incidents. Use when: session start (health check), sprint retro (trend analysis), before a Sentrux audit (verify no open P0)."
compatibility: "Cursor IDE, any SKILL.md-compatible agent. Requires: Grafana :3000 (admin), Prometheus :9090, Alertmanager :9093, observability/ sidecar running."
---

# Metrics Dashboard

Consolidates Helixon observability signals into a single performance
report. Companion to the Grafana dashboards provisioned in v14512.

## When to Use

- **Session start** — confirm `control_plane_up = 1` and no open P0
- **Sprint retro** — weekly trend on self-eval score, error ratio,
  cost USD/hr per tier
- **Sentrux audit pre-flight** — required input for v14515 / v14521
  closeout documents
- **After a major change** — was the rollout healthy?

## Data Sources

| Source | URL | What |
| ------ | --- | ----- |
| Grafana | `http://localhost:3000` | qwen36-fleet / control-plane / agentrace-traces panels |
| Prometheus | `http://localhost:9090` | raw metrics via PromQL |
| Alertmanager | `http://localhost:9093/api/v2/alerts` | firing + silenced alerts |
| Cost obs (v14511) | `~/.local/state/helixon/cost.ndjson` | per-call USD estimates |
| Self-eval | `reports/eval-runs/*.json` | scoreboard from eval-smoke runs |

## Procedure

1. **Health check** (3 queries):
   ```
   curl -s http://localhost:9090/api/v1/query?query=max(control_plane_up)
   curl -s http://localhost:9090/api/v1/query?query=sum(qwen36_cell_ready)
   curl -s http://localhost:9093/api/v2/alerts | jq '[.[] | select(.status.state=="active")] | length'
   ```
   Stop with a CRITICAL report if control_plane_up == 0 or any P0
   is `active`.
2. **Trend (1h / 24h / 7d)**:
   - `avg_over_time(agentrace_self_eval_score[1h])`
   - `avg_over_time(sum(rate(agentrace_stage_total{status="error"}[5m]))[24h:5m])`
   - `sum(rate(qwen36_est_cost_usd_total[1h])) * 3600`
3. **Cost rollup**:
   - parse `cost.ndjson` (from v14511 costobs), aggregate by `model`
   - flag any model whose 24h cost is > 2x trailing 7d mean
4. **Incidents**: pull Alertmanager silenced + firing, attribute to
   sprint id (labels carry `sprint: v145XX`), summarise.
5. **Emit** the report to `reports/metrics/YYYY-MM-DD.md` (one per day).

## Report Template

```markdown
# Helixon Performance Report — YYYY-MM-DD

## Executive Summary
<1-2 sentence overall health assessment>

## Control Plane
| Metric | Value | Target | Status |
| --- | --- | --- | --- |
| control_plane_up | 1 | 1 | OK |
| Open sprints | N | — | — |

## qwen36 Fleet
| Cell | Ready | p99 latency | GPU mem free | Cost USD/hr |
| --- | --- | --- | --- | --- |
| C1 | ✓ | 1.2s | 18 GiB | 0.42 |
| ... |

## Agentrace
| Metric | Value | Target | Status |
| --- | --- | --- | --- |
| Mean self-eval score | 0.78 | ≥ 0.7 | OK |
| Error ratio (24h) | 3.1% | ≤ 5% | OK |
| Tokens/hr | 124k | baseline 80k | WARN |

## Open Incidents
- P1: Qwen36LowGpuMemory on C5 — silenced 2026-07-15T14:00Z, ack by self-eval agent
```

## Acceptance

- Report is generated before any Sentrux audit closeout.
- Control plane UP = 1, no P0 firing, mean self-eval ≥ 0.7.
- Cost rollup matches `costobs` NDJSON within 5% drift.