# v14515 — Sentrux pair-6 audit — Handoff

Sprint: **v14515 Sentrux pair-6 audit**
Date: 2026-07-15
Repo: `helixon-platform`
Branch: `feature/v14515-sentrux-p6-audit` (rebased on `main`)
PR: pending (pair-lock to be closed in this commit)
Release tag: `sentrux-2026-07-29` (will push on planned date 2026-07-29)

## 1. Goal (from plan file, line 334)

> v14515 Sentrux pair-6 audit + token-saving strategy doc; rtx +
> context-mode + headroom enforcement; MacBook doc sweep across
> `session-handoffs/`, `global-memories/`, `docs/`; release tag
> `sentrux-2026-07-29`.

## 2. Deliverables shipped

### 2.1 Token-saving strategy doc (`docs/token-saving-strategy.md`)

Four-layer defense documented:
- L1 tier routing (already shipped v14510/v14511)
- L2 context trim (`internal/contextmode`)
- L3 headroom (`internal/headroom`)
- L4 RTX cache (`internal/rtx`)

Includes roll-out matrix, SLO/breach rules, cost observability,
enforcement matrix, carry-forward items.

### 2.2 ADR bundle (`docs/adr/0002-token-saving-enforcement.md`)

Documents the v14515 decision to add the 4-layer enforcement,
the wire-format change (`DecideInput.ReplayID`), and rejected
alternatives (cloud prompt caching, vector DB cache).

### 2.3 MacBook doc sweep

- `helixon-platform/` sweep: 1 substantive MacBook reference removed
  (the SQL `host_kind` enum comment in ADR 0001).
- Sweep filter excludes "retired", "decommission", "history" lines
  and the verifier file itself (avoids self-match).
- `cursor-global-kb/` sweep deferred to **v14518** (per plan line 350,
  the workspace-hygiene sprint sweeps `cursor-global-kb`).

### 2.4 RTX cache package (`internal/rtx/`)

NDJSON-backed response cache:
- Key: `fnv64a(prompt + stateHash)` (64-bit FNV, deterministic).
- TTL: 1h for tier ≥ 2, 24h for tier < 2 (configurable per call).
- Replay ID bypass: identical `replay_id` within TTL returns the
  most recent record regardless of prompt.
- Concurrent-safe (file lock during write, in-memory map for reads).
- **6 tests, all race-clean.**

### 2.5 Context-mode package (`internal/contextmode/`)

Stateless trim helpers:
- `Strip`: removes ANSI escapes, NUL runs, >200-char base64 blobs.
- `Truncate`: caps output at `maxTokens*4` chars with a marker.
- `FormatImportPath`: prepends a `[context-mode]` hint when the
  pasted content matches a `file:line..line` pattern (encourages
  the model to `Read` the file instead of using the duplicate paste).
- Idempotent: re-running on already-trimmed text is a no-op.
- **9 tests, all passing.**

### 2.6 Headroom package (`internal/headroom/`)

- Per-cell budget table (`DefaultBudgets`): `qwen36-27b-q4/q8/mtp`,
  `opus/sonnet-class-remote`, `local-echo`.
- `Check(cellID, requiredTokens)` returns `*HeadroomError` with
  budget + required when over the cell's available headroom.
- Fails safe on unknown cells (defaults to 32k context).
- `EstimateTokens(s)` is a 1-token-per-4-char heuristic.
- **6 tests, all passing.**

### 2.7 Choosehook enforcement (`internal/choosehook/enforce.go`)

`EnforceConfig.Enforce(base, in, promptTokens)` runs the full
v14515 pipeline:
1. `contextmode.Trim` the prompt.
2. `rtx.Cache.Lookup` — if hit, return `EnforceOutput{CacheHit: true,
   Reason: "rtx-replay"}`.
3. `headroom.Check(cellID, promptTokens)` — if reject, return
   `DecisionLabel="rejected"` + `RejectReason`.
4. `rtx.Cache.Store` the new record (best-effort).

Also added `DecideInput.ReplayID` field (backward compatible —
v14511 callers still work; the new field is ignored if absent).
**6 enforcement tests, all race-clean.**

## 3. Verification

### 3.1 Go test summary

```
internal/rtx          6/6 PASS (race-clean)
internal/headroom     6/6 PASS (race-clean)
internal/contextmode  9/9 PASS (race-clean)
internal/choosehook  11/11 PASS (race-clean, includes 6 Enforce_*)
```

### 3.2 Tier-4 cross-layer verifier

```
verifier: PASS=89  FAIL=0
RESULT: at/above v14511 bar (>= 53 PASS)
```

8 new v14515 checks:
- `token-saving: docs/token-saving-strategy.md present`
- `rtx: Go tests green`
- `headroom: Go tests green`
- `contextmode: Go tests green`
- `choosehook: enforcement tests green`
- `wire-format: DecideInput.ReplayID present`
- `macbook-sweep: zero live refs in helixon-platform/`
- `carry-forward: v14515 entries appended`

### 3.3 Saved evidence

```
reports/eval-runs/eval-run-v14515-01-tier4.json   # tier-4 verifier output
```

## 4. Cross-cutting compliance

| Rule | Status | Evidence |
| --- | --- | --- |
| Pair-lock | ✅ | `.sprint_lock` at branch start; will remove at close |
| Vendor verification | ✅ | No new vendor; FNV + tiktoken-free estimation |
| TDD-first | ✅ | All 4 new packages ship with tests written first |
| IaC/CaC | ✅ | `docs/token-saving-strategy.md` + `0002-*.md` are in-repo |
| Idempotency | ✅ | `contextmode.Trim` and `rtx.Store` are idempotent |
| 4xx/5xx retry | n/a | no external API |
| DB migration sequencing | n/a | no schema changes |
| No shell leaks | ✅ | All helpers are pure Go; no shell escaping |
| Token saving | ✅ | This sprint IS the token-saving enforcement |
| Carry-forward register | ✅ | 5 v14515 entries appended |

## 5. Carry-forward to v14517/v14521

- `semble` (semantic cache, opt-in): needs local 4B embedder in
  `qwen36-matrix.yaml`. Deferred to v14521.
- `cursor-global-kb` MacBook sweep: deferred to v14518 (per plan).
- `enforce.go` library → binary integration in
  `choose-llm hook decide`: deferred to v14517.
- Per-tenant budget cap enforcement: needs `agent-card.yaml`'s
  `budget_usd_per_day` (v14516).
- Replace LCG jitter in `internal/notify` with `crypto/rand`:
  reroll deferred.

## 6. Files added / updated in v14515

```
docs/token-saving-strategy.md                            # NEW
docs/adr/0002-token-saving-enforcement.md                # NEW
docs/adr/0001-control-plane-schema.md                    # MOD (MacBook sweep)
internal/rtx/rtx.go + rtx_test.go                        # NEW
internal/headroom/headroom.go + headroom_test.go          # NEW
internal/contextmode/contextmode.go + contextmode_test.go # NEW
internal/choosehook/enforce.go + enforce_test.go          # NEW
internal/choosehook/choosehook.go                        # MOD (ReplayID)
scripts/agentcage/install-tier4-verify.sh                # MOD (+8 v14515 checks)
carry-forward/carry-forward-register-2026-07-15.ndjson   # MOD (+5 v14515 items)
reports/eval-runs/eval-run-v14515-01-tier4.json          # NEW
session-handoffs/v14515-handoff.md                       # NEW (this file)
```

## 7. Restart prompt for v14516

> Continue with v14516 Pair 7 MVP: helixon-fleet-agents repo; personas
> (code-reviewer, ops-engineer, sre, release-manager); agent-card.yaml
> + skill bundle + hook config. Pair-lock against `main`. Re-run
> `scripts/agentcage/install-tier4-verify.sh` and confirm no regression.
> Use `gh repo create helixon-fleet-agents --public` if not yet created;
> otherwise initialize as a sibling repo.