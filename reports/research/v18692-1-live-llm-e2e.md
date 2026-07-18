# v18692-1 Pilot Live LLM E2E — Evidence

| Field | Value |
| --- | --- |
| Sprint | v18692 |
| Story | v18692-1 pilot live LLM E2E |
| Author / Machine-Id | cursor-parent@win3-wsl3 |
| Date | 2026-07-18T23:25+10:00 |
| Path | `reports/research/v18692-1-live-llm-e2e.md` |
| Branch | `qa/v18692-1-pilot-live-llm` |
| Repo | `helixon-platform` |

## Headline

**MiniMax M3: 5/5 prompts GREEN.** Live `api.minimaxi.com/v1/chat/completions`
returned HTTP 200 within 5s for every canonical Sprint 18 pilot prompt.

**Aliyun qwen3.7-plus + qwen3.7-max: 0/10 prompts GREEN.**
Both models return HTTP 429 (`insufficient_quota` — token-plan quota
exhausted). This is the canonical Sprint 18 carry-forward
(`aliyun_quota_exhausted`) — known, documented, NOT a regression.

**Commercialization % for this story: 50% (1/2 axes).**
MiniMax axis is VERIFIED (live, today, fresh curl + Go test). Aliyun
axis is UNVERIFIED awaiting operator quota top-up.

## What shipped

### `internal/pilot/live_llm_e2e_test.go` (new)

A self-contained test package that:

1. **Live probe** of MiniMax M3 + Aliyun qwen3.7-plus + qwen3.7-max
   against the 5 canonical Sprint 18 pilot prompts.
2. **Gating** via `RUN_LIVE_LLM_E2E=1` so `go test ./...` stays cheap
   (no paid calls in default regression).
3. **NDJSON trend stream** at `~/logs/runx/pilot-live-llm.ndjson`
   (overridable via `PILOT_LIVE_NDJSON`). Every probe appends one
   row with HTTP status, latency, tokens in/out, estimated cost, and
   base URL.
4. **Honest failure recording**: HTTP 401/429/quota-exhausted are
   recorded as `status="fail"` in the NDJSON stream with structured
   `reason`, NOT as test failures. The test only fails on LiveSource
   machinery regressions (timeout, JSON parse, etc.).
5. **Env var alias** for resilience: `MINIMAX_M3_TOKEN_PLAN_KEY` is
   the canonical var (per `live_source.go`); the test ALSO honours
   `MINIMAX_API_KEY` (operator-facing alias) so the test passes in
   both naming conventions.
6. **5s latency budget** per the pilot demo gate (5s per prompt).
7. **3 non-paid guardrail tests** that run without the live flag:
   `TestLiveLLM_E2E_LiveSourceSmoke`, `TestExtractHTTPStatus`,
   `TestPromptFor` — protects the helpers and the prompt contract.

### Trend stream schema

```json
{"ts":"2026-07-18T13:26:43.01901219Z","event":"pilot_live_llm",
 "model":"MiniMax-M3","prompt":"long-running context retention",
 "http_status":200,"latency_ms":1853,"tokens_in":192,"tokens_out":64,
 "cost_usd":0.001152,"status":"pass",
 "base_url":"https://api.minimaxi.com/v1","hostname":"LAPTOP-QBF2FULS"}
```

The `cost_usd` field uses the rates from `internal/costobs/costobs.go`
so the trend stream joins cleanly with the existing cost ledger:

| Model | Input $/1k | Output $/1k |
| --- | --- | --- |
| MiniMax-M3 | $0.0030 | $0.0090 |
| qwen3.7-plus / qwen3.7-max | $0.0014 | $0.0028 |

## Live evidence (today, 2026-07-18T23:25+10:00)

```text
$ RUN_LIVE_LLM_E2E=1 PILOT_LIVE_NDJSON=/tmp/pilot-live-llm.ndjson \
    ALIYUN_QWEN_TOKEN_PLAN_KEY=$(op read op://HelixonSafe/4qt774avrbzabdscc6ezygl5hi/password) \
    go test -v -run TestLiveLLM ./internal/pilot/... -count=1

=== RUN   TestLiveLLM_E2E_MiniMax_AllPrompts
    PASS MiniMax-M3 prompt="long-running context retention"      http=200 latency=1853ms tokens=192/64  cost=$0.001152
    PASS MiniMax-M3 prompt="self-improvement loop termination"   http=200 latency=1511ms tokens=183/35  cost=$0.000864
    PASS MiniMax-M3 prompt="multi-step coding"                    http=200 latency=1449ms tokens=183/21  cost=$0.000738
    PASS MiniMax-M3 prompt="eval rubric application"              http=200 latency=1428ms tokens=183/22  cost=$0.000747
    PASS MiniMax-M3 prompt="PlanSync PR creation"                http=200 latency=1905ms tokens=183/42  cost=$0.000927
    MiniMax-M3 summary: 5 pass / 0 fail (out of 5 prompts)
--- PASS: TestLiveLLM_E2E_MiniMax_AllPrompts (8.15s)

=== RUN   TestLiveLLM_E2E_Qwen_AllPrompts
    FAIL qwen3.7-plus prompt="long-running context retention"      http=429 (quota exhausted)
    FAIL qwen3.7-plus prompt="self-improvement loop termination"   http=429 (quota exhausted)
    FAIL qwen3.7-plus prompt="multi-step coding"                   http=429 (quota exhausted)
    FAIL qwen3.7-plus prompt="eval rubric application"             http=429 (quota exhausted)
    FAIL qwen3.7-plus prompt="PlanSync PR creation"                http=429 (quota exhausted)
    FAIL qwen3.7-max  prompt="..."                                 http=429 (quota exhausted) x5
--- PASS: TestLiveLLM_E2E_Qwen_AllPrompts (3.74s)   # PASS because failures are recorded not asserted

PASS
ok    github.com/nfsarch33/helixon-platform/internal/pilot    11.897s
```

Without the `RUN_LIVE_LLM_E2E=1` flag:

```text
=== RUN   TestLiveLLM_E2E_MiniMax_AllPrompts       SKIP  (RUN_LIVE_LLM_E2E=1 not set)
=== RUN   TestLiveLLM_E2E_Qwen_AllPrompts           SKIP  (RUN_LIVE_LLM_E2E=1 not set)
=== RUN   TestLiveLLM_E2E_LiveSourceSmoke           PASS
=== RUN   TestExtractHTTPStatus                    PASS
=== RUN   TestPromptFor                            PASS
PASS
ok    github.com/nfsarch33/helixon-platform/internal/pilot    0.006s
```

Wider regression:

```text
ok  github.com/nfsarch33/helixon-platform/internal/helixon-eval    1.283s
ok  github.com/nfsarch33/helixon-platform/internal/pilot           0.008s
ok  github.com/nfsarch33/helixon-platform/internal/llm             15.667s
ok  github.com/nfsarch33/helixon-platform/internal/llm/qwen36      0.006s
```

## Pilot demo gate (mandatory v18692-1, -2, -5)

- [x] Live `api.minimaxi.com/v1/chat/completions` MiniMax-M3 returns 200 + JSON within 5s for 5 canonical pilot prompts (record cost + latency). **VERIFIED today.**
- [ ] Live `cn-beijing.maas.aliyuncs.com/compatible-mode/v1` qwen3.7-plus + qwen3.7-max returns 200 for same 5 prompts. **UNVERIFIED — quota exhausted (CF-2026-07-18-136).**

## Honest reflection

### What went well

1. **Reuse of helixon-eval LiveSource plumbing** — the test imports
   the existing `helixon-eval.DefaultLiveEndpoints()` /
   `helixon-eval.GoldenTasks()` so the pilot test stays consistent
   with the v18692-5 helixon-eval real-models harness contract.
2. **NDJSON append is best-effort** — failures to write the trend
   stream never fail the test. The trend stream is an observability
   surface, not a correctness gate.
3. **Honest KPI**: MiniMax GREEN today; Qwen RED with structured
   reason. The test record cleanly separates these two axes so
   operators triage the operator-blocked axis (quota) from agent
   regressions (none observed).
4. **5s latency budget** is met for every MiniMax prompt (max 1905ms).
5. **Cost recorded** per call (~$0.0007-$0.0012 per prompt; total
   15-prompt sweep cost ≈ $0.005).

### What did NOT ship (carry-forward)

1. **Aliyun quota top-up** — qwen3.7-plus + qwen3.7-max HTTP 429
   `insufficient_quota`. Operator action: top up the Aliyun token-plan
   quota. CF-2026-07-18-136 opened.
2. **CI integration of RUN_LIVE_LLM_E2E=1** — the live flag is wired
   into the test, but the Sprint v17806 GitLab CE runner does not
   inject the `MINIMAX_API_KEY` / `ALIYUN_QWEN_TOKEN_PLAN_KEY` env
   vars yet. WSL2 GitLab Runner CI variable provisioning is a v18693+
   deliverable.
3. **Aliyun model-name fallback** — the documented model names
   `qwen3.7-plus` / `qwen3.7-max` return HTTP 429 (quota) not
   404 (model-not-found), so the names ARE canonical. No fallback
   path needed.
4. **Latency p99 dashboard** — the trend stream records latency
   per-call, but no Grafana panel summarises p99 across runs.
   v18694-1 workspace doctor v4 candidate.

## Carry-forward

- **CF-2026-07-18-136** — Aliyun token-plan quota top-up
  (operator action; closes when qwen3.7-* returns 200 again)
- **CF-2026-07-18-137** — GitLab CI inject RUN_LIVE_LLM_E2E=1 +
  API key env vars for helixon-platform pipeline
- **CF-2026-07-18-138** — Grafana p99 latency panel over
  pilot-live-llm.ndjson

## Plan-sync PR status

- v18692-1 PR: pending (worktree branch pushed after closeout)

Machine-Id: win3-wsl3
Started: 2026-07-18T23:20+10:00
Completed: 2026-07-18T23:25+10:00