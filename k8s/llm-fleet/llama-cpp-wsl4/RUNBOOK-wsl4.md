# v14887 Operator Runbook — llama.cpp CUDA on win4/wsl4

**Audience**: operator (jason@desktop-p5bul0f-wsl4)
**Time required**: ~30 min

## Pre-flight

```bash
# Verify GPU + CUDA
nvidia-smi --query-gpu=name,memory.total --format=csv
# Expected: NVIDIA GeForce RTX 3090, 24576 MiB
nvcc --version | grep release
# Expected: release 12.0+

# Verify model file present
ls -la /home/jason/models/qwen36-gguf/Qwen3.6-27B-Q4_K_M.gguf
# Expected: ~16.8 GB
```

## Step 1 — Install nvidia-container-toolkit

wsl4 is the only fleet host that lacks this. Required for
GPU passthrough into podman containers.

```bash
# Add NVIDIA repo
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | \
    sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
    sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
    sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

sudo apt-get update
sudo apt-get install -y nvidia-container-toolkit
```

## Step 2 — Configure CDI for podman

```bash
sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
sudo nvidia-ctk runtime configure --runtime=podman

# Verify
podman info 2>&1 | grep -A2 cdi
# Expected: cdi: true
```

## Step 3 — Smoke test GPU passthrough

```bash
podman run --rm --device nvidia.com/gpu=all \
    docker.io/nvidia/cuda:12.8.1-base-ubuntu24.04 nvidia-smi -L

# Expected output:
# GPU 0: NVIDIA GeForce RTX 3090 (...)
```

## Step 4 — Pull and start the llama-cpp container

The container image is built by CI on win1/wsl1 and pushed to
`ghcr.io/nfsarch33/llama-cpp-cuda:dev`. Until the CI pipeline
finishes (post-GitLab CE recovery), pull the locally-built
image from the build host, or build from the Containerfile:

```bash
# Option A — build locally (if no image push yet)
cd /home/jason/Code/llama-cpp
podman build -t ghcr.io/nfsarch33/llama-cpp-cuda:dev \
    -f Containerfile.cuda.debian-slim .

# Option B — pull from GHCR (post-recovery)
podman pull ghcr.io/nfsarch33/llama-cpp-cuda:dev
```

## Step 5 — Deploy via Quadlet

```bash
sudo cp /home/jason/Code/llama-cpp/quadlet/llama-cpp-quadlet.container \
        /etc/containers/systemd/llama-cpp.container

sudo systemctl daemon-reload
sudo systemctl enable --now llama-cpp.service
sudo systemctl status llama-cpp.service
```

## Step 6 — Verify HTTP endpoint

```bash
# Wait for model load (16GB → ~30s)
sleep 45

# Local test
curl -sf http://127.0.0.1:11435/health
# Expected: {"status":"ok"}

# External test (from win1/wsl1)
curl -sf http://desktop-p5bul0f-wsl4.tail447712.ts.net:11435/health
```

## Step 7 — Sanity inference test

```bash
curl -s -X POST http://127.0.0.1:11435/v1/chat/completions \
    -H "Content-Type: application/json" \
    -d '{
      "model": "/models/Qwen3.6-27B-Q4_K_M.gguf",
      "messages": [{"role":"user","content":"Reply with the single word HELIXON"}],
      "max_tokens": 32,
      "temperature": 0
    }' | jq .choices[0].message.content

# Expected: "HELIXON" (or similar acknowledgment)
```

## Step 8 — Wire into llm-cluster-router (v14888)

After this runbook is complete, the router will discover
the peer at `desktop-p5bul0f-wsl4.tail447712.ts.net:11435`
automatically (via the bootstrap peer list).

## Rollback

```bash
sudo systemctl disable --now llama-cpp.service
sudo rm /etc/containers/systemd/llama-cpp.container
sudo systemctl daemon-reload
```

## Common failures

| Symptom | Cause | Fix |
|---|---|---|
| `nvidia-container-cli: not found` | toolkit not installed | Re-run step 1+2 |
| `Error: could not select device driver "" with capabilities: [[utility compute]]` | CDI not generated | `sudo nvidia-ctk cdi generate` |
| `Error: mounting /models: operation not permitted` | SELinux on host | `:z` or `:Z` flag in Volume |
| Container exits with exit code 1 immediately | libcuda not visible | Check `lsmod \| grep nvidia` |