---
name: agent-self-evaluation
description: "SRE-grade self-evaluation skill that watches Helixon fleet agent health (self-eval score, error rate, token cost) and acknowledges or escalates P0/P1 SLO breaches from observability/alertmanager. Use when: a PagerDuty or Slack alert fires, when starting a session to confirm no open incidents, or when asked 'is everything healthy?'."
compatibility: "Cursor IDE, Claude Code, any SKILL.md-compatible agent. Requires: Prometheus metrics on :9090, Alertmanager on :9093, PagerDuty + Slack webhook already configured in observability/alertmanager.yml."
---

# Agent Self-Evaluation

Watches the Helixon observability stack and acknowledges or escalates
alerts fired by `observability/alerts/prometheus-helixon-alerts.yml`.

## When to Use

- Alertmanager fires a P0/P1 alert and you need to triage
- Session start: confirm no open P0 incidents before doing anything else
- After a sprint close: verify all alerts resolved within SLO
- Before a release tag (sentrux-*): confirm dashboard-runbook compliance

## Inputs (Prometheus queries)

| Metric | Query |
| ------ | ----- |
| Open P0 alerts | `ALERTS{alertstate="firing",severity="page"}` |
| Open P1 alerts | `ALERTS{alertstate="firing",severity="notify"}` |
| Self-eval score | `avg(agentrace_self_eval_score)` |
| Error ratio (1h) | `sum(rate(agentrace_stage_total{status="error"}[1h])) / clamp_min(sum(rate(agentrace_stage_total[1h])), 1e-9)` |
| Cost surge | `sum(rate(qwen36_est_cost_usd_total[1h])) * 3600` |

## Procedure (per pair-pair acceptance criteria)

1. **Poll** Alertmanager (`curl -s http://localhost:9093/api/v2/alerts`).
   Group by `severity`. Stop only when there are no `firing` P0 alerts.
2. **Acknowledge** each P0 alert via Alertmanager API:
   ```
   amtool silence add --alertmanager.url=http://localhost:9093 \
     --duration=30m --comment="self-eval ack by helixon-agent" \
     --matchers="alertname=<NAME>,severity=page"
   ```
   The silence window buys time to triage without re-paging on-call.
3. **Triage** the underlying condition:
   - `Qwen36ZeroCellsReady` / `ControlPlaneDown` → call `fleet-doctor`
     skill for affected node; restart the cell / pod.
   - `Qwen36High5xx` → tail `qwen36_request_total{status="5xx"}` by
     `cell_id`; identify the cell and rotate it out.
   - `AgentraceHighErrorRate` → review `agentrace_stage_total` by
     `classification` and route to the matching persona's runbook.
4. **Resolve** by either (a) fixing the root cause and clearing the
   silence, or (b) opening a follow-up ticket and leaving the silence
   active with a 4h repeat_interval reminder.
5. **Document** the resolution in `session-handoffs/<sprint>-incidents.md`
   with the alert name, time-to-acknowledge, root cause, and link to
   the runbook used.

## Outputs

- One row appended to `session-handoffs/incidents.ndjson` per ack:
  ```
  {"ts":"2026-07-15T16:00:00Z","alertname":"Qwen36High5xx","severity":"page",
   "sprint":"v14513","ack_within_min":4,"root_cause":"cell C3 OOM",
   "runbook":"docs/runbooks/qwen36-cell-rotation.md"}
  ```
- A markdown summary in the session handoff if a P0 fired during
  the sprint window.

## Acceptance

- Every P0 alert fired during the sprint has an `incidents.ndjson`
  row within 5 minutes of `firing` state.
- No alert remains in `firing` state at sprint close.
- Mean self-eval score stays ≥ 0.6 (from the agentrace dashboard).