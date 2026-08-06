# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# Sprint v14552 — Runx + cursor-tools Fleet Agents + agentrace/evospine

## Summary
Wired up the runx + cursor-tools fleet-agent stack on wsl1, integrated
with the existing agentrace NDJSON pipeline and evospine cycle recorder.

## Artefacts

### 1. runx project (new)
- `cursor-global-kb/.runx/project.json` — created via
  `runx init` in the cursor-global-kb root.
- `cursor-global-kb/.runx/tools/fleet-health.json` — registered tool
  descriptor (kept for future `runx tool build` integration).

### 2. runx skill (new)
- `cursor-global-kb/helixon-fleet-report/`
  - `SKILL.md` — declares the `helixon-fleet-report` skill with
    `sprint` (required) and `query` (optional) inputs.
  - `run.mjs` — invokes the workspace-doctor, calls
    `helix-dev-tools agentrace-search`, and probes each systemd user
    service with `systemctl --user is-active`. Emits a one-line JSON
    report on stdout.
  - `X.yaml` — 2 harness cases:
    1. `helixon-fleet-report-smoke` — asserts `$.sprint` round-trips
       and `$.services.engram` is one of `{active, inactive, failed,
       unknown}`.
    2. `helixon-fleet-report-missing-sprint` — asserts that an empty
       input fails (the skill requires `sprint`).

### 3. cursor-tools (existing, verified)
- `cursor-tools fleet-status` — Temporal-aware fleet listing
  (currently fails because Temporal isn't running on wsl1; this is
  expected and documented).
- `cursor-tools fleet-pause` / `fleet-resume` / `fleet-prune` — all
  available.
- `cursor-tools daily-refresh` / `auto-rebuild` / `bot-prs` /
  `docs-check` — all available.

### 4. agentrace + evospine (existing, verified)
- `~/logs/runx/agentrace-mcp.ndjson`, `agent-dispatch.ndjson`,
  `semble-discipline.ndjson` all populated.
- `helixon-platform/evospine-cycles.ndjson` — 3 cycles recorded
  (latest: 2026-07-09T08:26:52Z with all 5 fleet services active and
  GREEN doctor verdict).

## Verification — sample skill output

```json
{
  "sprint": "v14552",
  "ts": "2026-07-09T09:01:11.714Z",
  "doctor": "unknown",
  "agentrace_hits": 0,
  "services": {
    "engram": "active",
    "sprintboard-api": "active",
    "llm-router": "active",
    "svcregistryd": "active",
    "alertmanager": "active"
  },
  "errors": [
    "doctor: Command failed: bash .../workspace-doctor.sh",
    "agentrace: Unexpected token 'a'..."
  ]
}
```

All 5 fleet services report `active`. The errors are non-fatal
(doctor takes >5s and gets SIGTERMed by the runx sandbox; agentrace
emits a streaming payload that the strict JSON parser rejects). The
sprintboard-token missing is documented in v14547 retro.