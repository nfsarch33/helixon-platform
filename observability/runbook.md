# Helixon Observability Runbook

Authored: 2026-07-15 (v14513 Pair-5 Review).
Owner: SRE persona (Helixon fleet agent).
Audience: on-call engineer + Helixon fleet agents.

This runbook is the single source of truth for what to do when a P0/P1
alert from `observability/alerts/prometheus-helixon-alerts.yml` fires.

## Triage flow

```
+----------------------+   +----------------------+
| Alert fires          |   | Self-eval agent ack  |
| (PagerDuty or Slack) |-->| within 5 min (P0)    |
+----------------------+   +----------------------+
                                     |
                                     v
                       +-------------------------+
                       | Open runbook section    |
                       | matching alert name     |
                       +-------------------------+
                                     |
                                     v
                       +-------------------------+
                       | Apply fix or open       |
                       | follow-up ticket        |
                       +-------------------------+
                                     |
                                     v
                       +-------------------------+
                       | Confirm metric recovers |
                       | (Prometheus graph)      |
                       +-------------------------+
                                     |
                                     v
                       +-------------------------+
                       | Append incidents.ndjson |
                       | + close silence         |
                       +-------------------------+
```

## Alert runbook matrix

### `Qwen36ZeroCellsReady` (P0 — page)

| Step | Command | Expected |
| --- | --- | --- |
| 1 | `bash /home/jaslian/Code/helixon-platform/scripts/agentcage/install-tier4-verify.sh` | PASS ≥ 53 |
| 2 | `/tmp/choose-llm matrix list --matrix /home/jaslian/Code/cursor-global-kb/scripts/fleet/qwen36-matrix.yaml` | non-empty ready cells |
| 3 | Per `cell_id` not in `ready`: `systemctl status llama-server@<cell_id>` (or `podman ps -a`) | find exited OOM |
| 4 | `journalctl -u llama-server@<cell_id> --since -10m \| tail -50` | root cause OOM / GPU fall-off |
| 5 | Restart cell: `systemctl restart llama-server@<cell_id>` | wait 30s |
| 6 | Re-check: `/tmp/choose-llm pick --tier 2 --matrix <yaml>` | returns cell_id |

### `Qwen36High5xx` (P0 — page)

| Step | Command | Expected |
| --- | --- | --- |
| 1 | `curl -s 'http://localhost:9090/api/v1/query?query=sum%20by%20(cell_id)(rate(qwen36_request_total%7Bstatus%3D~%225..%22%7D%5B5m%5D))'` | top offender cell_id |
| 2 | `/tmp/choose-llm matrix list` | identify the cell's `host:port` |
| 3 | `curl -s http://<host>:<port>/healthz` | confirm down |
| 4 | Take cell out of rotation: edit `qwen36-matrix.yaml`, set `status: blocked`, commit, push | choose-llm no longer routes to it |
| 5 | Restart per cell runbook above | |
| 6 | Restore cell status when healthy | |

### `ControlPlaneDown` (P0 — page)

| Step | Command | Expected |
| --- | --- | --- |
| 1 | `systemctl status helixon-control-plane` | confirm exited |
| 2 | `journalctl -u helixon-control-plane --since -5m \| tail -100` | root cause |
| 3 | If SQLite lock: `fuser /var/lib/helixon/control-plane.db` | find stale writer |
| 4 | `systemctl restart helixon-control-plane` | wait 10s |
| 5 | `curl -sf http://localhost:8080/healthz` | returns 200 |
| 6 | `curl -sf http://localhost:8080/readyz` | returns 200 |

### `FleetHeartbeatsStale` (P1 — notify)

| Step | Command | Expected |
| --- | --- | --- |
| 1 | `curl -s 'http://localhost:9090/api/v1/query?query=time%20-%20control_plane_fleet_node_last_seen_seconds%20%3E%20600'` | list silent nodes |
| 2 | Per node: `ssh <node> 'uptime; tailscale status'` | check mesh + load |
| 3 | If mesh OK, force heartbeat: `ssh <node> '/usr/local/bin/helixon-heartbeat --once'` | resets timer |
| 4 | Document in incidents.ndjson; silence 4h | |

### `AgentraceHighErrorRate` (P0 — page)

| Step | Command | Expected |
| --- | --- | --- |
| 1 | `curl -s 'http://localhost:9090/api/v1/query?query=sum%20by%20(classification)(rate(agentrace_stage_total%7Bstatus%3D%22error%22%7D%5B15m%5D))'` | dominant error classification |
| 2 | If `429_too_many_requests` → back off the matching tier | |
| 3 | If `5xx_upstream` → run `Qwen36High5xx` runbook above | |
| 4 | If `context_overflow` → reduce `agentrace_tokens_total` per stage, see `docs/runbooks/context-overflow.md` | |
| 5 | If unknown → page the SRE persona | |

### `AgentraceLowSelfEvalScore` (P1 — notify)

| Step | Command | Expected |
| --- | --- | --- |
| 1 | `curl -s 'http://localhost:9090/api/v1/query?query=avg%20by%20(persona)(agentrace_self_eval_score)'` | identify under-scoring persona |
| 2 | Read latest `reports/eval-runs/*.json` for that persona | find regression |
| 3 | Roll back the persona's last release if regression is post-release | |
| 4 | Open a follow-up ticket for the persona's owner | |

## Self-eval agent contract (matches `agent-self-evaluation` skill)

1. Poll Alertmanager every 30s while a P0 is firing.
2. First P0 ack within 5 minutes; subsequent P1 within 15 minutes.
3. Append a row to `session-handoffs/incidents.ndjson` per ack:
   ```json
   {"ts":"2026-07-15T16:00:00Z","alertname":"...","severity":"page",
    "sprint":"v14513","ack_within_min":4,"root_cause":"...",
    "runbook":"observability/runbook.md#<anchor>"}
   ```
4. Close the silence when the underlying metric recovers (or open a
   follow-up ticket and extend the silence to 4h).

## Metrics dashboard contract (matches `metrics-dashboard` skill)

1. Generate a daily report at `reports/metrics/YYYY-MM-DD.md`.
2. The report is a required input for any Sentrux audit closeout
   (v14515 / v14521).
3. Cost rollup must reconcile with `costobs` NDJSON within 5% drift.

## Acceptance for v14513 close

- `observability/skills/agent-self-evaluation/SKILL.md` committed.
- `observability/skills/metrics-dashboard/SKILL.md` committed.
- `observability/runbook.md` (this file) committed.
- `observability/verify-observability.sh` updated with paging-wiring checks.
- Tier-4 verifier still ≥ 53 PASS.
- One end-to-end ack drill run captured in
  `reports/eval-runs/eval-run-v14513-01-ack-drill.json`.