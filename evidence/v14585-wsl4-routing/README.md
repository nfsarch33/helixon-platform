# v14585 — Wire wsl4 into llm-cluster-router

**Sprint**: v14585 (Pair 5 Review)
**Status**: COMPLETED (with 3 carry-forwards)
**Date**: 2026-07-10

## Summary

This sprint wires the wsl4 RTX 3090 LLM cell (CF since v14530, unblocked by v14584
which surfaced the existing vllm Qwen2.5-7B-Instruct-AWQ) into the llm-cluster-router
running on wsl1.

The wire-up was originally expected to use qwen3.6-27b Q8_0 (29 GB) on wsl4, but
wsl4 only has a single 24 GB RTX 3090 — insufficient for the 29 GB model. Per the
operator's CF-v14584-01 resolution, we wired the existing 7B AWQ vllm cell as
c5-wsl4 (tier 1 fallback) instead.

## What works

| Test | X-Tier | Model | Routed to | Status |
|------|--------|-------|-----------|--------|
| 1 | 0 | Qwen3.6-27B-MTP-Q8_0 | c7-wsl1 (llama.cpp port 8010) | 200 OK |
| 2 | 1 | Qwen/Qwen2.5-7B-Instruct-AWQ | c5-wsl4 (vllm port 8001) | 200 OK |
| 3a | (none) | Qwen3.6-27B-MTP-Q8_0 | c7-wsl1 | 200 OK |
| 3b | (none) | Qwen/Qwen2.5-7B-Instruct-AWQ | c5-wsl4 | 200 OK |

## Discoveries / findings

1. **node.cfg.Tier is exact-matched against X-Tier header** — ranges like `"0..3"`
   never match anything. Each node must declare a single tier string (`"0"`, `"1"`, etc).
2. **node.cfg.Models is exact-matched against request `model` field** — substring
   match is NOT supported in `internal/router/helpers.go::SupportsModel`.
3. **The `routes:` block in YAML is parsed but not applied** by the current
   llm-cluster-router binary. The router only honors X-Tier header routing.
4. **Health probe fallback works**: a 404 on `/health` triggers a retry on `/v1/models`,
   which Ollama returns 200 for.
5. **URL with `/v1` suffix breaks health probe**: because `target.Path = baseURL.Path
   + path` produces `/v1/health` then `/v1/v1/models` (404). Always use base URL
   without trailing path for Ollama nodes.
6. **The current binary (`llm-router` v14547) does NOT register `/v1/embeddings`**,
   so all embedding requests return 404 from the router itself. This is a binary
   regression vs the source tree. Documented as CF-v14585-02.

## Carry-forwards

- **CF-v14585-01**: `routes:` block in YAML not applied. Either remove from config
  to avoid operator confusion, or patch the binary to honor routes. Rec. remove.
- **CF-v14585-02**: Tier 2/3 nodes (Ollama chat + embeddings) fail in current binary
  because `/v1/embeddings` is not registered and Ollama-tier probes don't converge.
  Rebuild from source with the embedding handler, OR drop c6-ollama entirely and
  let direct Ollama access via `http://localhost:11434` cover embedding needs.
- **CF-v14585-03**: 27B Q8_0 GGUF (~29 GB) does not fit single RTX 3090 (24 GB).
  Carried from v14584. Possible resolutions: (a) wait for wsl5 (RTX 5090 32 GB);
  (b) quantize to Q4_K_M (~16 GB) for single-GPU fit; (c) shard across two GPUs
  on a future wsl5+ setup. Current 7B AWQ on c5-wsl4 is the operational fallback.

## Files changed

- `cursor-global-kb/configs/llm-cluster-router.yml` — replaced `tier: "0..3"` ranges
  with explicit single-tier strings (`"0"`, `"1"`, `"2"`), pruned fictitious nodes
  (c2-wsl1, c8-wsl1-ollama-chat) that don't have backing upstreams, fixed node URLs
  for proper health probing.
- `evidence/v14585-wsl4-routing/routing-test.txt` — captured all routing tests
  including successful tier 0/1 dispatches to c7-wsl1 and c5-wsl4.

## Vendor verification (re-confirmed for this sprint)

- **Qwen3.6-27B-MTP-GGUF** — upstream `Qwen/Qwen3-27B` on HuggingFace (verified).
- **llama.cpp** — upstream `ggerganov/llama.cpp` on GitHub.
- **vllm** — upstream `vllm-project/vllm` on GitHub (serving c5-wsl4).
- **llm-cluster-router** — fork `nfsarch33/llm-cluster-router` (forked upstream).

No new dependencies or vendors introduced in this sprint.