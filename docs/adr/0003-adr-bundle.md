# 0003 — ADR Bundle (v14504–v14521 closeout)

**Status:** Accepted
**Date:** 2026-07-15
**Sprint:** v14521 Sentrux pair-9 FINAL
**Owner:** Helixon release manager

## Scope

This bundle supersedes the standalone ADRs in `docs/adr/` and
consolidates the architectural decisions taken across the 18-sprint
roadmap into a single reviewable artifact.

## Decisions

### 1. ADR-0001 — Control-plane schema (`docs/adr/0001-control-plane-schema.md`)

**Decision:** Control-plane uses a JSON-encoded `.sprint_lock` with
schema_version, pair_id, sprint_id, phase, started_at, closed_at, pid,
operator, optional personas. **Status:** Accepted.

### 2. ADR-0002 — Token-saving enforcement (`docs/adr/0002-token-saving-enforcement.md`)

**Decision:** A 4-layer defense: (1) tier routing via choose-llm,
(2) context trimming via contextmode, (3) headroom enforcement via
headroom, (4) RTX cache via rtx. **Status:** Accepted.

### 3. ADR-0003 (this document) — ADR Bundle

**Decision:** Standalone ADRs are subordinate to this bundle for
review purposes. The bundle is updated at every Sentrux audit. New
ADRs are added to this bundle and to `docs/adr/000N-*.md`. **Status:**
Accepted.

## Cross-cutting decisions (not in dedicated ADRs but documented here)

### Paired-sprint pattern

- One MVP + one Review sprint per pair, sharing the same `pair_id`.
- Sentrux audits close pairs every ~2 weeks.
- Pair-lock enforced via `tools/pair-lock/` (v14517).

### Fleet-agents repo split

- Personas live in `helixon-fleet-agents/`; control-plane lives in
  `helixon-platform/`.
- A persona references its home repo via `persona.home_repo`.
- Cross-repo orchestration via Sprintboard MCP (deferred to v14522+).

### Eval-harness ground truth

- 10-prompt smoke (`eval-harness/prompts-10.json`) is the canonical
  "does the eval pipeline still work?" check.
- Mock mode returns deterministic pass/fail based on prompt ID slot.
- Live mode requires `OPENAI_API_KEY` or `QWEN_API_KEY`; not used in CI.

### Cost observability

- NDJSON at `~/.cache/helixon/costobs/events.ndjson`.
- Schema: `{ts, prompt_id, model, input_tokens, output_tokens,
  estimated_usd, job_id, persona_id}`.
- Append-only; aggregation done by Grafana / metrics-dashboard skill.

### Observability sidecar

- Prometheus + Grafana + Alertmanager in a single Docker Compose.
- Three Grafana dashboards: qwen36-fleet, control-plane,
  agentrace-traces.
- P0/P1 SLO alerts in `prometheus-helixon-alerts.yml`.

### MCP inventory

- 21 MCP servers documented in
  `cursor-config/mcp/cursor-tools-inventory.json`.
- `cursor-tools` CLI provides inventory, doctor, restore commands.

## Accepted-with-deferred-items

| Item | Defer-to |
| --- | --- |
| Mint Helixon-bot GitHub App | v14522 |
| Cursor-global-kb → GitHub migration | v14522+ |
| Cycle driver Go port | v14522 if latency matters |
| Per-persona budget tracking | v14522+ |
| Sprintboard MCP integration | v14522+ |

## Review

- Sendrux pair-9 audit (2026-08-12) approved.
- Next audit: pair-10 in approximately 2 weeks.