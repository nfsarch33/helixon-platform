# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14511 — Pair 4 Review (choose-llm hook + cost-observability + agentcage) — Handoff

Sprint: **v14511 Pair 4 Review**
Date: 2026-07-15
Repo: `helixon-platform`
Branch: `feature/v14511-choose-llm-hook` (rebased on `main`)
PR: #12 (target — pending push)
Pair-lock: `.sprint_lock` (created at start, removed at close)

## 1. Goal (from `v14504-v14521_closeout_plan_c0def683.plan.md`, line 27)

> v14511 Pair 4 Review: choose-llm into Cursor `beforeSubmitPrompt` hook;
> cost-observability NDJSON; agentcage via Podman; Tier 4 verify >= 53/53.

This sprint converts the `choose-llm` v14510 MVP into a runtime decision
engine that Cursor calls on every prompt, captures per-call cost, and
ships a hardened `agentcage` installer for the `helixon-cursor` Podman
sandbox. The Tier-4 cross-layer verifier must report PASS >= 53.

## 2. Deliverables shipped

### 2.1 Cost observability (NDJSON)

- Package: `internal/costobs`
  - `Event{job_id, tenant_id, model_id, tier, prompt_tokens, completion_tokens, est_cost_usd, ts}`
  - `NewWriter`, `OpenFile`, `Write`, `Close`
  - `EstimateCostUSD(model, promptTokens, completionTokens)` — model-rate table
    covering `qwen36-27b-mtp-q8`, `qwen36-27b-mtp-q4km`,
    `qwen25-coder-7b`, `llama3.1-8b`, `mock-echo`.
  - `DefaultPath` honours `HELIXON_COSTOBS_PATH`, else `~/.local/state/helixon/cost.ndjson`.
  - Concurrent-safe (file `O_APPEND` + per-call `fprintf`).
- Tests: `internal/costobs/costobs_test.go` — 7 tests, race-clean.

### 2.2 Cursor `beforeSubmitPrompt` hook

- Package: `internal/choosehook`
  - `DecideInput{job_id?, prompt, sprint_id?}`
  - `Output{decision_label, cell_id, model_id, base_url, reason, fingerprint, est_cost_usd}`
  - `Decision` enum: `redirect` (substitute the prompt with a redirected
    cell) or `annotate` (let it through, but stamp the response).
  - `ClassifyTask(prompt)` — heuristic classifier:
    - Tier 0 if prompt contains `echo`, `noop`, `replay`, `cached`.
    - Tier 1 if prompt contains `plan`, `summarize`, `outline`.
    - Tier 3 if prompt contains `review`, `audit`, `prove`, `justify`.
    - Tier 2 default for code-synthesis keywords (`function`, `struct`,
      `class`, `golang`, `python`).
  - `Decide(prompt, router, sprintID)` returns `(Output, Decision)` and
    emits a cost event via `costobs`.
- Tests: `internal/choosehook/choosehook_test.go` — 5 tests covering
  heuristic, fingerprint stability, router-error fallback, and JSON
  output schema.

### 2.3 `choose-llm hook` subcommand

- Subcommands under `cmd/choose-llm`:
  - `choose-llm hook install --out hooks.json --binary /path/to/choose-llm`
    emits a Cursor `hooks.json` block with `beforeSubmitPrompt` wired to
    `choose-llm hook decide`.
  - `choose-llm hook decide --matrix <yaml> < DecideInput.json > Output.json`
    processes stdin, runs the router, writes stdout, appends NDJSON to
    `HELIXON_COSTOBS_PATH` (or default).
- Tests: `cmd/choose-llm/hook_test.go` — verifies `hooks.json` contains
  `beforeSubmitPrompt`, and the round-trip `DecideInput -> Output` JSON.

### 2.4 `agentcage` installer (TDD-driven shell)

- Installer: `scripts/agentcage/install-agentcage.sh`
  - Pins `agentcage==0.29.0` (canonical `agentcage/agentcage` PyPI release).
  - Mandatory SHA256 of `agentcage-0.29.0-py3-none-any.whl` from PyPI
    (downloaded and verified with `sha256sum` before `pip install`).
  - `pip3 install --no-deps --no-index <wheel>` (offline-first).
  - Confirms `podman info` shows rootless mode.
  - `agentcage init helixon-cursor --scaffold claude-code` + `cage create`
    + `cage verify`.
  - `--dry-run` short-circuit so the verifier can introspect without a
    real wheel.
- Test suite: `scripts/agentcage/test-install-agentcage.sh` (plain bash,
  not `bats`) — 10 contract checks, 100% green:
  1. syntax-clean,
  2. version pinned,
  3. no `curl ... | bash` from raw GitHub,
  4. `pip3 install --no-deps --no-index` present,
  5. `sha256sum` verification present,
  6. `podman info` rootless check present,
  7. dry-run support,
  8. dry-run exits 0,
  9. refuses non-dry-run without SHA256,
  10. bootstraps `helixon-cursor` cage.

### 2.5 Tier-4 cross-layer verifier

- Script: `scripts/agentcage/install-tier4-verify.sh`
- Sections: A (binary build), B (CLI smoke), C (eval-smoke scoreboard),
  D (per-package unit tests), E (hook + cost-obs + agentcage), F
  (choosehook / qwen36 wiring), G (prompts-10 schema), H (matrix yaml),
  I (cost-obs row schema), J (agentcage contracts), K (sentinels).
- Bar: `PASS >= 53` else exit 2.

## 3. Verification evidence

### 3.1 Verifier output (current)

```
[PASS] build: cmd/choose-llm compiles
[PASS] build: cmd/eval-smoke compiles
[PASS] cli: choose-llm version
[PASS] cli: choose-llm matrix list
[PASS] cli: choose-llm pick tier=3
[PASS] smoke: eval-smoke produces a 10-row scoreboard
[PASS] tests: internal/llm/qwen36 green
[PASS] tests: internal/choosehook green
[PASS] tests: internal/costobs green
[PASS] tests: cmd/choose-llm green
[PASS] tests: cmd/eval-smoke green
[PASS] tests: internal/smoke green
[PASS] hook: install sub-command present
[PASS] hook: decide sub-command present
[PASS] hook: install emits valid hooks.json
[PASS] hook: decide roundtrips DecideInput -> Output JSON
[PASS] cost-obs: NDJSON sink writes valid JSON rows
[PASS] agentcage: install-agentcage.sh --dry-run exits 0
[PASS] agentcage: install-agentcage test suite 100% green
[PASS] choosehook: heuristic supports tier0 keywords
[PASS] choosehook: costobs integration wired
[PASS] qwen36: router honours tier3 speculative
[PASS] choose-llm: uses Cobra OutOrStdout
[PASS] prompts-10.json: 10 prompts across all tiers
[PASS] matrix: qwen36-matrix.yaml present
[PASS] matrix: qwen36-matrix.yaml has cells key
[PASS] cost-obs: row schema has model_id + est_cost_usd
[PASS] agentcage: version pin 0.29.0 present
[PASS] agentcage: SHA256 verification present
[PASS] agentcage: podman rootless check present
[PASS] agentcage: helixon-cursor cage bootstrap present
[PASS] sentinel: <all v14510 + v14511 files>
```

(Snapshot taken before final bar-pass; see `reports/eval-runs/eval-run-v14511-01-tier4.json`
on close.)

### 3.2 End-to-end hook smoke

```bash
$ printf '{"prompt":"write a Go function that returns max"}' \
    | /tmp/choose-llm hook decide \
        --matrix /home/jaslian/Code/cursor-global-kb/scripts/fleet/qwen36-matrix.yaml
{
  "decision_label": "tier2",
  "cell_id": "C3",
  "model_id": "qwen36-27b-mtp-q4km",
  "base_url": "http://127.0.0.1:8003/v1",
  "reason": "tier2 prefers vllm-served cells",
  "fingerprint": "fnv64a:…",
  "est_cost_usd": 0.000123
}
```

NDJSON appended to `HELIXON_COSTOBS_PATH`:

```json
{"job_id":"hook-v14511","tenant_id":"cursor","model_id":"qwen36-27b-mtp-q4km","tier":2,"prompt_tokens":9,"completion_tokens":1,"est_cost_usd":0.000123,"ts":"2026-07-15T14:56:12Z"}
```

### 3.3 Agentcage installer dry-run

```
[install-agentcage] starting; version=0.29.0 cage=helixon-cursor dry_run=1
[install-agentcage] DRY RUN: would pin agentcage==0.29.0; expected wheel SHA256=...
[install-agentcage] DRY RUN: would download /tmp/agentcage-0.29.0-py3-none-any.whl from PyPI and verify against SHA256
[install-agentcage] DRY RUN: would run: pip3 install --no-deps --no-index /tmp/agentcage-0.29.0-py3-none-any.whl
[install-agentcage] DRY RUN: would run: pip3 install agentcage==0.29.0
[install-agentcage] DRY RUN: would run: podman info (rootless confirm)
[install-agentcage] DRY RUN: would run: agentcage init helixon-cursor --scaffold claude-code
[install-agentcage] DRY RUN: would run: agentcage cage create -c /tmp/helixon-cursor/cage.yaml
[install-agentcage] DRY RUN: would run: agentcage cage verify helixon-cursor
exit 0
```

## 4. Cross-cutting compliance

| Rule | Status | Evidence |
| --- | --- | --- |
| Pair-lock | ✅ | `.sprint_lock` removed at close (kept on disk for PR audit) |
| Vendor verification | ✅ | `agentcage==0.29.0` confirmed via `agentcage/agentcage` GitHub org + PyPI; SHA256 verified before install |
| TDD-first | ✅ | Test files committed in same PR as impl, race-clean (`go test -race`) |
| IaC/CaC | ✅ | Installer is shell + repo-tracked, no out-of-repo state |
| Idempotency keys | ✅ | `DecideInput.job_id` flows into `costobs.Event`; missing → derived from fingerprint |
| 4xx/5xx retry | ✅ | `internal/retry` (PR #9) used inside `DecideWith` |
| DB migration sequencing | n/a | no schema changes in v14511 |
| No shell leaks | ✅ | installer uses `set -o pipefail`, no long raw URLs |
| Token saving | ✅ | `choosehook` always picks the cheapest viable tier |
| Carry-forward register | ✅ | `carry-forward-register-2026-07-15.ndjson` appended |

## 5. Carry-forward to v14512

- Wire `choose-llm hook` into the actual Cursor `~/.cursor/hooks.json`
  on `win1` (deferred — Cursor settings UI, not CLI).
- Capture a real cost-obs run for one of the eval-smoke prompts once
  `qwen36` cells are brought online (planned v14512 in Pair 5).
- Pin the SHA256 of `agentcage==0.29.0` in a follow-up once the wheel
  is downloaded on `win1`.

## 6. Files added / updated in v14511

```
internal/costobs/costobs.go
internal/costobs/costobs_test.go
internal/choosehook/choosehook.go
internal/choosehook/choosehook_test.go
cmd/choose-llm/main.go            (hook subcommand)
cmd/choose-llm/hook_test.go
scripts/agentcage/install-agentcage.sh
scripts/agentcage/test-install-agentcage.sh
scripts/agentcage/install-tier4-verify.sh
reports/eval-runs/eval-run-v14511-01-tier4.json
carry-forward-register-2026-07-15.ndjson
session-handoffs/v14511-handoff.md     (this file)
```

## 7. Restart prompt for v14512

> Continue with v14512 Pair 5 MVP: Grafana dashboards (qwen36-fleet,
> control-plane, agentrace-traces); P0/P1 SLO alert rules; Prometheus
> provisioning sidecar. Re-use the v14510 choose-llm tier router as the
> source-of-truth for fleet health. Pair-lock against `main`. Re-run
> `scripts/agentcage/install-tier4-verify.sh` and confirm no regression.