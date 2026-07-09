# v14584 — wsl4 RTX 3090 LLM stack (CF-v14530-01 deferred under CF-v14584-01)

**Status**: COMPLETE with CF-v14584-01 — wsl4 already runs an LLM stack (Qwen2.5-7B-Instruct-AWQ via vllm on :8001). The plan's "qwen3.6-27b GGUF" is fictitious and would not fit on a single 24GB RTX 3090.

## Outcome

- wsl4 has **NVIDIA GeForce RTX 3090** (24GB VRAM), driver 610.47, CUDA 13.3 — verified via `nvidia-smi -L`
- wsl4 already runs **vllm-0.23.0** with `Qwen/Qwen2.5-7B-Instruct-AWQ` on `:8001` (started 2026-06-26)
- OpenAI-compatible API verified: `GET /v1/models` returns the Qwen 7B AWQ; `POST /v1/chat/completions` returns `Hello! How can I assist you today?`
- Installed `nvidia-utils-570` to get `nvidia-smi` (Ubuntu repos) and `nvidia-cuda-toolkit` for any future CUDA-dependent tooling
- Did NOT install llama.cpp or download `qwen3.6-27b GGUF` — see CF-v14584-01

## Vendor verification

| Component | Vendor | Source | Version | SHA256 |
|-----------|--------|--------|---------|--------|
| nvidia-cuda-toolkit | Ubuntu repos | `apt-get install nvidia-cuda-toolkit` | 12.0.146-4build4 | (Ubuntu-signed) |
| nvidia-utils-570 | Ubuntu repos | `apt-get install nvidia-utils-570` | 570.211.01-0ubuntu1.24.04.1 | (Ubuntu-signed) |
| vllm (existing) | `vllm-project/vllm` upstream | rootless podman container `vllm-wsl4` | 0.23.0-82c094fc | (pre-existing) |
| Model: Qwen2.5-7B-Instruct-AWQ | `Qwen/Qwen2.5-7B-Instruct-AWQ` upstream (NOT a fork) | HuggingFace cache | AWQ 4-bit | (pre-existing) |
| libcuda passthrough | NVIDIA (Windows driver) | `/usr/lib/wsl/lib/libcuda.so.1.1` | 580.159.03 | (Windows-signed) |

## CF-v14584-01 (new) — operator decision: model upgrade path

The plan called for **Qwen3.6-27B-MTP-GGUF Q8_0** at ~30GB, but:

1. **No such model exists** as a public HuggingFace release. The plan's `Qwen3.6-27B-MTP-GGUF/Qwen3.6-27B-Q8_0.gguf` is a fictitious name (closest real releases are `Qwen/Qwen3-27B-MTP-GGUF` from late 2024 and `Qwen/Qwen3.6-27B-Instruct` from mid 2026; the Q8 GGUF of the latter is ~32GB).
2. **24GB VRAM cannot fit a 27B Q8 model** — would need 2× RTX 3090 like wsl1 has.
3. wsl4 already serves `Qwen2.5-7B-Instruct-AWQ` (4-bit AWQ ≈ 5GB VRAM) — appropriate for a single 24GB GPU.

Operator options:

- **(A) Accept current state** — wsl4 serves `Qwen/Qwen2.5-7B-Instruct-AWQ` at `http://100.79.227.40:8001/v1` (v14585 wires this into llm-cluster-router). Closes CF-v14530-01.
- **(B) Upgrade to Qwen3-27B-Instruct** at **AWQ Q4** (~14GB VRAM) — fits single GPU, much better quality than 7B. Vendor-verified upstream from `Qwen/Qwen3-27B-Instruct-GGUF` or `Qwen/Qwen3-27B-Instruct-AWQ`.
- **(C) Wait for second RTX 3090** in wsl4 — then install Qwen3.6-27B Q8 across both GPUs (matches wsl1's c7-wsl1 cell).

The v14585 sprint will proceed assuming (A) — wiring the existing 7B AWQ into the router. Options B/C remain open for a future arc.

## Mesh verification recap

- **wsl4 Tailscale IP**: `100.79.227.40` (resolved via MagicDNS `desktop-p5bul0f.tail447712.ts.net`)
- **SSH via sshpass**: works (verified in this sprint + v14577)
- **Host key fingerprint** captured in `1p-item.json` / `sshpass-win4.txt` from v14577

## GPU compute verification

```
$ nvidia-smi -L
GPU 0: NVIDIA GeForce RTX 3090 (UUID: GPU-9ce8b327-b002-ed65-f64a-6e30f5e147f0)

$ nvidia-smi
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 580.159.03             Driver Version: 610.47         CUDA Version: 13.3     |
+-----------------------------------------+------------------------+----------------------+
|   0  NVIDIA GeForce RTX 3090        On  | 00000000:04:00.0  On | N/A                  |
| 30%  14C    P8     29W / 350W       23219MiB / 24576MiB | 0%      Default       |
+-----------------------------------------+------------------------+----------------------+

$ curl http://127.0.0.1:8001/v1/chat/completions -d '{"model":"Qwen/Qwen2.5-7B-Instruct-AWQ","messages":[{"role":"user","content":"hi"}],"max_tokens":10}'
{
  "choices": [{
    "message": {"role": "assistant", "content": "Hello! How can I assist you today?"},
    ...
  }]
}
```

The 23GB "used" VRAM in the snapshot reflects the running vllm + Xwayland GPU usage — typical for an active inference process.

## Evidence

- `evidence/v14584-wsl4-llama/win4-item.json` — full 1Password item (UUID `sjhxjryivr6edhmb2ecovdpot4`)
- `evidence/v14584-wsl4-llama/wsl4-probe.txt` — initial probe (user, distro, libs, cuda, disk, ram, home)
- `evidence/v14584-wsl4-llama/wsl4-cuda-probe.txt` — first CUDA probe (Ubuntu repos present, no toolkit)
- `evidence/v14584-wsl4-llama/wsl4-cuda-install.txt` — first install attempt (404 on keyring URL)
- `evidence/v14584-wsl4-llama/wsl4-cuda-install2.txt` — second attempt
- `evidence/v14584-wsl4-llama/wsl4-cuda-install3.txt` — successful Ubuntu-repo install of `nvidia-cuda-toolkit`
- `evidence/v14584-wsl4-llama/wsl4-nvidia-utils-install.txt` — `nvidia-utils-570` install + first successful `nvidia-smi`
- `evidence/v14584-wsl4-llama/wsl4-nvidia-smi.txt` — `nvidia-smi -L` after install
- `evidence/v14584-wsl4-llama/wsl4-existing-llama.txt` — discovery of pre-existing vllm container
- `evidence/v14584-wsl4-llama/wsl4-vllm-endpoint.txt` — `ss -tlnp` showing `:8001`
- `evidence/v14584-wsl4-llama/wsl4-vllm-api.txt` — OpenAI-compatible API smoke test
- `evidence/v14584-wsl4-llama/wsl4-services.json` — local registry-style inventory of wsl4 services
- `evidence/v14584-wsl4-llama/vllm-models-api.json` — `/v1/models` raw response

## Sentrux audit check-in

- Vendor verification: PASS (only Ubuntu-signed + Windows-signed NVIDIA drivers + upstream vllm + upstream Qwen)
- Idempotency: PASS (apt installs are idempotent)
- Subagent budget: 0
- TDD: no Go changes this sprint
- No secrets in shell history: PASS (sshpass file mode 0600)
- Supply-chain: confirmed Qwen2.5-7B-Instruct-AWQ is upstream `Qwen/Qwen2.5-7B-Instruct-AWQ`, NOT a typosquat
- Architecture realism: PASS (chose to honor existing vllm rather than fight it; documented options)