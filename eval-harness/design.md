# eval-harness design (v14510 MVP)

**Status:** design (MVP shipped in v14510; review in v14511)
**Owner:** helixon-platform
**Sprint:** v14510
**Companion binary:** `cmd/choose-llm`
**Companion data:** `cursor-global-kb/scripts/fleet/qwen36-matrix.yaml`

## Goal

Every tier (`tier0`..`tier3`) of the `choose-llm` router must be
backed by an executable, evidence-anchored evaluation harness. The
harness must:

1. Pick **10 prompts** that are representative of the tier's typical
   workload. Each prompt has a ground-truth expected response shape.
2. Hit the cell that `choose-llm pick --tier=N` returns for each
   prompt and capture the response.
3. Score the response with a deterministic rubric:
   - JSON validity (if expected-JSON),
   - substring/keyword presence,
   - byte-length bound,
   - latency budget (seconds from start of request to last byte),
   - non-empty response and no error.
4. Emit an evidence JSON that mirrors
   `session-handoffs/evidence/v256-d5-qwen36-smoke-skeleton/qwen36-eval-smoke.json`
   so existing tooling (`cursor-tools evoloop emit-outcome`,
   Grafana, EvoSpine) can consume the output unchanged.

The 10-prompt smoke is the **minimum** required for v14510 closeout.
The full harness (v14511+) extends to:

- 50-prompt regression set
- judge model cross-validation (qwen3.7-plus / MiniMax-M3)
- cost-per-1k-tokens cost attribution
- dead-letter queue on 3-consecutive same-prompt failure (per global
  rule "max 3-5 retries then DLQ").

## Tier-by-tier workload model

| Tier | Label | Workload | Why this model |
| ---- | ----- | -------- | -------------- |
| 0 | cheap-drafting | Short JSON-shape requests, summarisation, regex extraction | Latency-bound (sub-200ms target); needs smallest model that can still hold 4k context |
| 1 | heuristic-planning | Multi-step plan synthesis, rubric scoring, tool selection | Needs 16k-65k context to keep plan + scratch-pad in one window |
| 2 | code-synthesis | Long diffs, large file edits, multi-file refactors | vLLM continuous batching wins for parallel agent traffic; vram headroom ok |
| 3 | deep-review-reasoning | Reasoning, spec compliance check, security audit | Speculative decoding (MTP) lifts throughput ~2x vs Q4_K_M |

These come from the v14502-02 real eval run (`reports/eval-runs/eval-run-v14502-01-minimax-m3.md`): the 7 task-type set was already
classified into the same four bands during the `DefaultSemanticRouterConfig`
work in `internal/llm/semantic_router.go:50`.

## 10-prompt smoke (v14510 deliverable)

The 10 prompts are split across the 4 tiers. They are deterministic
(grammars and seed), short enough to fit in the smallest cell's
context window (8k for C8 9B / C5 4B), and cover the matrix's
existing tier-0..tier-3 mapping:

| # | Tier | Prompt | Expected shape |
| - | ---- | ------ | -------------- |
| 1 | 0 | "Return JSON {\"ok\": true}." | `{"ok":true}` |
| 2 | 0 | "Extract the email in 'ping jason@helixon.io please'." | `jason@helixon.io` |
| 3 | 0 | "Summarise in <=12 words: 'The qwen3.6 matrix lists 7 cells...'" | 1 sentence, <=12 words |
| 4 | 0 | "List 3 common Go pitfalls. JSON array." | `["x","y","z"]` |
| 5 | 1 | "Plan the migration from helm v2 to v3 in 5 bullets." | 5-line text |
| 6 | 1 | "Score 1..10 the prompt: '... eval harness ...'" | integer 1..10 |
| 7 | 1 | "Pick the cheapest cell in cursor-global-kb for tier0." | text mentions "C1" |
| 8 | 2 | "Write a Go func that returns the larger of two ints." | text contains "func " and "if " |
| 9 | 3 | "Audit this snippet for off-by-one: `for i:=0;i<=n;i++`." | text mentions "off-by-one" or "i<n" |
| 10 | 3 | "Justify why we picked Q8_0 MTP for tier3 reasoning." | text mentions "MTP" or "speculative" |

The prompts are saved at `eval-harness/prompts/01..10.json` so the
smoke is reproducible without re-typing.

## Rubric

Each prompt has a `rubric` block in its prompt JSON:

```json
{
  "id": "t0-01",
  "tier": 0,
  "prompt": "Return JSON {\"ok\": true}.",
  "rubric": {
    "exact_json": {"ok": true},
    "max_completion_tokens": 16,
    "max_latency_ms": 500
  }
}
```

The smoke runner (`tools/eval-smoke/`) reads the rubric, hits
`choose-llm pick --tier=N`, makes the OpenAI-compatible call, and
emits a per-prompt result. Aggregate report at
`reports/eval-runs/eval-run-v14510-01-tier-smoke.json`.

## Hardware-aware defaults

The harness defaults to:

- `--matrix /home/jaslian/Code/cursor-global-kb/scripts/fleet/qwen36-matrix.yaml`
- `--host-override 127.0.0.1` (operator wires `host-override` per env)
- `--timeout 30s` (allows for cold-start of llama.cpp first request)
- `--max-concurrency 1` (most cells hold one slot; C7 dual-GPU can do 2)

These defaults are documented in `tools/eval-smoke/cmd/eval-smoke/main.go`.

## Deferred to v14511+

The next sprint ships:

- judge model cross-validation (MiniMax-M3 + qwen3.7-plus + aliyun fallback)
- cost-per-1k-tokens row in the evidence JSON
- non-zero-exit-code on any prompt failure (>2 failures => DLQ)
- integration with helixon-eval (`cmd/helixon-eval run --matrix=qwen36-matrix.yaml`)

## References

- `cmd/choose-llm/main.go::pickOutput` -- the wire shape
- `internal/llm/qwen36/router.go::Pick` -- the tier scoring
- `internal/llm/semantic_router.go::DefaultSemanticRouterConfig` -- the original 4-tier mapping
- `session-handoffs/evidence/v256-d5-qwen36-smoke-skeleton/qwen36-eval-smoke.json` -- the existing evidence shape we mirror
- `cursor-global-kb/scripts/fleet/qwen36-matrix.yaml` -- the canonical matrix
- `reports/eval-runs/eval-run-v14502-01-minimax-m3.md` -- the workload classification source