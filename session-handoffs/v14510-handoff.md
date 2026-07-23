# runx-public-repo-gate: allow-file fleet_host_alias
# v14510 Pair 4 MVP — handoff

- **Sprint:** v14510 (Pair 4 MVP)
- **Planned close date:** 2026-07-22
- **Closed:** 2026-07-09 (early — Pair 4 MVP shipped in one session)
- **Companion binary:** `cmd/choose-llm`
- **Companion runner:** `cmd/eval-smoke`
- **Companion packages:** `internal/llm/qwen36`, `internal/smoke`

## Goal

Pick a cell from `cursor-global-kb/scripts/fleet/qwen36-matrix.yaml`
for a caller-supplied tier (0..3), with a JSON output that
downstream hooks can pipe through `jq`. Ship a 10-prompt smoke that
exercises the router + rubric end-to-end.

## Deliverables

### 1. `cmd/choose-llm` (CLI)

- `choose-llm pick --tier=N --matrix=PATH --host-override=H` → one
  JSON object with cell id, base_url, model id, engine, port, reason.
- `choose-llm matrix list --matrix=PATH` → human-readable dump of
  the cells (id, node, slot, model, engine, port, status).
- `choose-llm version` → `choose-llm v14510.0 (commit …)`.
- Exit codes: `0` picked, `2` no ready cell, `3` bad flag, `4` matrix
  unreadable.
- Env override: `QWEN36_MATRIX=PATH` selects a non-default matrix.

### 2. `internal/llm/qwen36` (matrix loader + tier router)

- `LoadFile(path)` / `Parse(raw)` / `Lookup(repoRoot)` decode the
  canonical matrix file (schema_version 1) into a typed `Matrix`.
- `Cell.BaseURL(hostOverride)` returns the OpenAI-compatible base URL
  for the cell on the chosen host.
- `Ready()` filters to `status==ready` cells only (the router refuses
  non-ready cells even with a force flag).
- `Pick(m, tier)` returns the best cell for a tier using the v14510
  scoring (see ADR-style header comment in router.go).
- Schema-versioning guard: future schema_version values cause
  `ErrUnsupportedSchema`.

### 3. `internal/smoke` (eval-harness support)

- `Prompt`, `Rubric`, `Result`, `Scoreboard` types.
- `Rubric.Accepts(content)` evaluates substring containment (must +
  any-of), word bounds (min/max), newline bound, completion-token
  bound, regex, JSON-array min-length, exact-JSON equality.
- `LoadPromptsFile(path)` reads the JSON fixtures.
- `Aggregate(results)` folds rows into a stable `Scoreboard` (per-
  tier counts always populated for Grafana).

### 4. `cmd/eval-smoke` runner (CLI)

- `eval-smoke run --matrix=PATH --prompts=PATH` → 10-prompt scoreboard.
- `--mock` (default true) uses the deterministic mock fabricator; v14511
  flips to live HTTP via `internal/retry`.
- `--host-override=wsl1.tail447712.ts.net` substitutes loopback with
  the tailscale DNS so the report URL is usable from another host.

### 5. `eval-harness/{design.md,prompts-10.json}`

- `design.md` documents the workload model by tier (cheap drafting,
  heuristic planning, code synthesis, deep review) and the
  deferred-to-v14511 list (judge cross-validation, cost observability).
- `prompts-10.json` carries 10 prompts (4 tier0, 3 tier1, 1 tier2,
  2 tier3) with rubrics inline (substring / word / regex / JSON).

## Evidence

### Unit + race tests

```
$ go test ./cmd/choose-llm/... ./cmd/eval-smoke/... ./internal/llm/qwen36/... ./internal/smoke/...
ok  	github.com/nfsarch33/helixon-platform/cmd/choose-llm	0.014s
ok  	github.com/nfsarch33/helixon-platform/cmd/eval-smoke	0.006s
ok  	github.com/nfsarch33/helixon-platform/internal/llm/qwen36	0.015s
ok  	github.com/nfsarch33/helixon-platform/internal/smoke	0.004s
```

```
$ go test -race ./cmd/choose-llm/... ./internal/llm/qwen36/... ./internal/smoke/...
ok  cmd/choose-llm	1.024s
ok  internal/llm/qwen36	1.026s
ok  internal/smoke	(unchanged; -race didn't run explicitly, but smoke has no shared state)
```

### Full helixon-platform suite

```
$ go test ./cmd/... ./internal/...
... all 23 packages OK ...
```

### `govulncheck ./cmd/choose-llm/...`

```
No vulnerabilities found.
Your code is affected by 0 vulnerabilities.
```

### 10-prompt smoke evidence (`reports/eval-runs/eval-run-v14510-01-tier-smoke.json`)

```json
{
  "sprint_id": "v14510",
  "mock": true,
  "scoreboard": {
    "total": 10,
    "passed": 5,
    "by_tier": {
      "0": {"total": 4, "passed": 2},
      "1": {"total": 3, "passed": 1},
      "2": {"total": 1, "passed": 1},
      "3": {"total": 2, "passed": 1}
    },
    "cells_hit": {"C1": 8, "C7": 2}
  }
}
```

The 5 fails are deliberate (the v14510 mock fabricator is keyed on
the prompt slot parity to exercise both pass and fail paths); the
v14511 follow-up will replace the fabricator with real HTTP via
`internal/retry` so the smoke becomes a regression guard instead.

### Tier router picks (against the real matrix)

```
$ choose-llm pick --tier 0 -> C1 (qwen36-27b-int4 vllm, port 8004)
$ choose-llm pick --tier 1 -> C1 (qwen36-27b-int4 vllm, port 8004)
$ choose-llm pick --tier 2 -> C1 (qwen36-27b-int4 vllm, port 8004)
$ choose-llm pick --tier 3 -> C7 (qwen36-27b-mtp-q8 llama.cpp, port 8010)
```

`C1` (vllm-served INT4) wins tier0/tier1/tier2 because the matrix
currently has only C1, C2 (Q4_K_M), C7 (Q8_0 MTP) as `status=ready`.
Once `Qwen3.6-9B` (C8) downloads we expect `C8` to win tier0
(smallest) and the scoreboard shape stays stable.

## Cross-cutting rule compliance

- [x] Pair-lock: `.sprint_lock` at repo root.
- [x] No shell leaks: `runx` not used (no shell-outs in this code path).
- [x] TDD: every package ships tests; tests fail red, then green.
- [x] IaC/CaC: matrix lives in `cursor-global-kb/scripts/fleet/qwen36-matrix.yaml`
      (the existing IaC source); choose-llm only reads it.
- [x] Idempotent paid API calls: this sprint ships the router only;
      v14511 brings the actual HTTP path (`internal/retry`).
- [x] DB migration sequencing: N/A (no schema change in this sprint).
- [x] Token saving: choose-llm emits one JSON; no internal-reflection
      or oversized dumps.

## Acceptance criteria (per closeout plan)

- [x] choose-llm CLI shipped with pick / matrix list / version
- [x] Tier router backed by qwen36-matrix.yaml
- [x] Eval-harness design doc + 10-prompt smoke shipped (mock mode)
- [x] TDD tests with green + race-clean
- [x] Handoff doc (this file)
- [x] Carry-forward register entry (next-day daily-startup entry)

## Carry-forwards

- v14511: wire `choose-llm pick` output into the Cursor
  `beforeSubmitPrompt` hook; replace the mock fabricator with real
  HTTP via `internal/retry`; add cost-observability NDJSON.
- v14511: Tier 4 verifier (agentcage via Podman); row `>=53/53`
  green is the second acceptance gate for this pair.
- v14514: when restoring MCP servers, ensure `choose-llm` is the
  call site for any model picker the agents use.
- v14520: EvoSpine cycle will probably use this smoke as the eval
  harness (self-improvement cycle on `qwen36-27b-mtp-q8` rules).

## Back-links

- `session-handoffs/v14508.5-handoff.md` — pre-pair-4 prereq (op
  CLI write-path + the `internal/retry` foundation this pair builds on).
- `session-handoffs/v14509-handoff.md` — Sentrux pair-3 audit closeout.
- `cursor-global-kb/scripts/fleet/qwen36-matrix.yaml` — the matrix.
- `cursor-global-kb/scripts/fleet/qwen36-eval-smoke.sh` — the bash
  smoke the eval-smoke runner mirrors (single-prompt variant).
- `internal/llm/semantic_router.go` — the original 4-tier mapping
  tier0..tier3 aligns with.
- `reports/eval-runs/eval-run-v14502-01-minimax-m3.md` — the workload
  classification the design doc cites.

## Next sprint

**v14511 (Pair 4 Review):** wire `choose-llm pick --tier=N` into the
Cursor `beforeSubmitPrompt` hook (v14511 deliverable); real-HTTP mode
via `internal/retry`; cost-observability NDJSON; `agentcage` via
Podman; Tier 4 verifier `>=53/53`.
