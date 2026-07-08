# ADR 0002: Token-saving enforcement (v14515)

- **Status:** Accepted
- **Date:** 2026-07-15
- **Deciders:** Helixon platform team (v14515 Sentrux pair-6 audit)

## Context

Cursor `beforeSubmitPrompt` hooks (v14511) chose the right LLM cell
but did not enforce three budget boundaries:

1. **Caching**: identical prompts in the same agent loop re-billed the cell.
2. **Context size**: tool outputs > 2k tokens pushed prompts past cell limits.
3. **Response headroom**: prompts that left < 1k tokens for the response
   produced truncated or hallucinated output.

Without enforcement, every agent turn spent more tokens than necessary.

## Decision

Adopt the **4-layer defense** documented in `docs/token-saving-strategy.md`:

- **L1 — Tier routing** (already shipped v14510/v14511)
- **L2 — Context trim** via `internal/contextmode` (strip ANSI / base64,
  truncate at 2k tokens, hint at `file:line..line` matches).
- **L3 — Headroom** via `internal/headroom` (reject prompts that don't
  leave 1024 / 4096 tokens for response on local / remote cells).
- **L4 — RTX cache** via `internal/rtx` (NDJSON-backed fnv64a cache,
  replay_id bypass, per-tier TTL).

The Cursor hook gains a new optional `replay_id` field; when present,
identical replay_ids skip re-billing within the TTL.

## Consequences

- Hook contract: `DecideInput.ReplayID` (string, optional). Backward
  compatible — v14511 callers still work.
- Wire format: `EnforceOutput` adds `cache_hit`, `cache_key`,
  `trimmed_bytes`, `reject_reason`. Cursor's hook reader ignores
  unknown fields.
- Cost observability: every cache hit + replay is logged to
  `reports/cost/cost-YYYY-MM-DD.ndjson` with `cell_id="cache"`.
- Test coverage: 6 rtx + 6 headroom + 9 contextmode + 6 enforce = 27
  new tests, all race-clean.

## Alternatives considered

- **Cloud-managed prompt cache (e.g., OpenAI prompt caching):**
  rejected — we're local-first and don't want vendor lock-in.
- **Vector DB cache (Pinecone / Chroma):** rejected — overkill for a
  fleet of 1-3 subagents; fnv64a + TTL is sufficient.
- **Per-prompt explicit `cache_ttl`:** rejected — adds friction without
  measurable benefit at our scale.

## Roll-out

| Sprint | Status |
| --- | --- |
| v14515 | library + tests shipped; binary integration deferred |
| v14517 | wire into `choose-llm hook decide` |
| v14520 | multi-agent coordination via EvoSpine cycle |
| v14521 | retro + closeout |