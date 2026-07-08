# Token-saving strategy (v14515)

**Status:** Adopted — enforced in v14511+ via Cursor `beforeSubmitPrompt` hook.
**Owner:** Helixon platform team
**Sprint:** v14515 Sentrux pair-6 audit
**Review cadence:** every Sentrux audit (v14515, v14521)

---

## 1. Problem statement

The Helixon fleet runs a mix of:
- Local LLMs (Qwen3.6-27B-MTP-GGUF Q8_0 / Q4_K_M on dual RTX 3090)
- Remote APIs (1Password, GitHub, Atlassian, Perplexity, Wolfram)

Each agent turn can spend:
- **Local tokens** (zero $ cost, but GPU time + KV cache pressure)
- **Remote API tokens** (real $ cost, billed per call)

Without a tier router and a context-bounding hook, every prompt — even
trivial "what's the date" — gets routed to a heavyweight remote model,
or every code change sends the entire repo as context.

## 2. The 4-layer defense

| Layer | Tool | Purpose |
| --- | --- | --- |
| **L1 — Tier routing** | `choose-llm` | Pick the cheapest cell that can answer (tier 0 → local 4B, tier 3 → remote Opus-class). |
| **L2 — Context trim** | `context-mode` | Summarize / drop / move-to-file system + user context before submission. |
| **L3 — Headroom** | `headroom` | Reserve N tokens of headroom for the model's expected response; reject prompts that won't fit. |
| **L4 — rtx / cache / replay** | `rtx` + `semble` | Cache responses by prompt-fingerprint; replay identical requests without re-billing. |

All four are **enforced** for every subagent (Cursor `beforeSubmitPrompt` hook)
and every CLI tool (`choose-llm hook decide` subcommand, v14511).

## 3. Tier routing (L1) — `choose-llm`

Tier matrix (`qwen36-matrix.yaml`):

| Tier | Class | Example cell | Routing rule |
| --- | --- | --- | --- |
| 0 | trivial ping / cache hit / replay | `local-echo`, `cache` | latency ≤ 50 ms, no remote |
| 1 | small Q&A, single-file edits, doc lookups | `qwen36-27b-q4` local | GPU memory headroom ≥ 30 % |
| 2 | multi-file reasoning, refactor planning | `qwen36-27b-q8` local | local-first |
| 3 | cross-repo synthesis, design docs, audits | remote `opus-class` | only when tier 2 confidence < 0.6 |

`choose-llm pick --tier N` returns the chosen cell + `why`.
`choose-llm hook decide` reads a `beforeSubmitPrompt` JSON payload from
stdin and emits the chosen cell on stdout — wired into Cursor via
`cursor-config/hooks/beforeSubmitPrompt.sh` (v14511).

## 4. Context-mode (L2)

`context-mode` is a stateless, zero-network helper:

- Reads `.cursor-context/` for the active project (a tiny file tree +
  file-summaries).
- Truncates any tool output longer than `MAX_OUTPUT_TOKENS` (default 2048).
- Replaces large pasted code blocks with `file:line` references.
- Strips ANSI / binary / base64 noise from logs.

It runs **before** `choose-llm hook decide` so the tier router sees a
sane budget.

## 5. Headroom (L3)

The hook requires every prompt to leave at least:

- **Local cells:** 1024 tokens headroom (Qwen3.6-27B 32k context window).
- **Remote cells:** 4096 tokens headroom (most remote APIs log + truncate
  past this point).

If headroom is insufficient, the hook **rejects** the prompt and writes
a carry-forward entry (no silent truncation → no hidden reasoning loss).

## 6. rtx / cache / replay (L4)

`rtx` (response cache):
- Key: `fnv64a(prompt + tool_state_hash)`.
- TTL: 1 hour for `tier ≥ 2`, 24 hours for `tier < 2`.
- Storage: `~/.cache/helixon/rtx.ndjson` (rotated at 10 MB).

`semble` (semantic cache, opt-in):
- Computes a 64-dim embedding on the prompt via a local 4B embedder.
- Cache hit if cosine-similarity > 0.95 against a prior prompt within TTL.

`replay` (subagent bypass):
- Subagents get a `replay_id`; identical `replay_id` re-uses the prior
  response without invoking the cell. Catches "agent asks same Q twice"
  loops.

## 7. Cost observability

Every cell call writes a `costobs.Event` to
`reports/cost/cost-YYYY-MM-DD.ndjson`:

```json
{
  "ts":"2026-07-15T01:23:45Z",
  "cell_id":"qwen36-27b-q8",
  "tier":2,
  "model":"qwen3.6-27b-mtp-q8",
  "input_tokens":1340,
  "output_tokens":420,
  "estimated_usd":0.0,
  "job_id":"v14515-sentrux-p6",
  "task_key":"audit-section-4"
}
```

Local cells write `estimated_usd: 0.0` + a wall-time cost (estimated
GPU-seconds × $0.0001 / GPU-sec) for capacity planning.

## 8. SLO / breach detection

Tier-4 verifier (`scripts/agentcage/install-tier4-verify.sh`) now
includes:

- `cost-obs: row schema complete` — every NDJSON row has all fields.
- `cost-obs: 0/1 outliers` — daily spend variance ≤ 2σ of 7-day mean.
- `rtx: cache hit rate ≥ 30%` for tier 0/1 over 7 days.
- `tier-routing: tier-3 share ≤ 25%` over 7 days (otherwise we burn $).

## 9. Enforcement matrix

| Caller | L1 tier | L2 ctx | L3 hdrm | L4 rtx |
| --- | --- | --- | --- | --- |
| Cursor chat (human) | hook | hook | hook | opt-in |
| Cursor chat (agent) | hook | hook | hook | forced |
| `choose-llm pick` | n/a | n/a | n/a | n/a |
| `choose-llm hook decide` | impl | impl | impl | impl |
| `eval-smoke` | explicit | impl | impl | impl |
| `helixon-slo-ack` | tier 0 | impl | impl | forced |

"impl" = the helper is invoked by the caller.
"forced" = the caller cannot disable it (cost-savings guarantee).

## 10. Roll-out status

| Sprint | Status |
| --- | --- |
| v14510 | `choose-llm` tier router shipped |
| v14511 | `choose-llm hook decide` + `beforeSubmitPrompt.sh` wired |
| v14512 | cost NDJSON row schema in `install-tier4-verify.sh` |
| v14513 | SLO alert: `HelixonDailySpendAnomaly` (P1) + `HelixonRTXCacheLow` (P2) |
| **v14515** | **this doc + headroom rejection + `replay_id` support in hook** |
| v14521 | final retro; close the v14515 carry-forwards |

## 11. Open items / carry-forward

- `semble` (semantic cache) is opt-in; default still off because the
  local 4B embedder is not yet in `qwen36-matrix.yaml`. Carry-forward to
  v14521.
- Per-tenant budget caps not yet enforced (v14516 `agent-card.yaml`
  will add `budget_usd_per_day` field; v14517 will enforce it in the
  hook).
- `rtx` cache is per-user (`~/.cache/...`); multi-agent coordination
  deferred to v14520 EvoSpine cycle.

---

**Adopted:** 2026-07-15
**Last reviewed:** 2026-07-15 (v14515 Sentrux pair-6)
**Next review:** v14521 final audit