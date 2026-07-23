# runx-public-repo-gate: allow-file fleet_host_alias,network_topology,secret_cred_ref
# Helixon Platform Deployment Guide

> **Status:** MVP-ready (Sprint v16041 added 2026-07-05).
> **Scope:** Production deploy for `helixon serve` on a single Linux host.

## 1. Prerequisites

### 1.1 System requirements

- Linux x86_64 (Ubuntu 22.04+ or RHEL 9+)
- 2 CPU, 4 GB RAM minimum (8 GB recommended for production)
- 10 GB disk for binary + memory DB + logs
- Network egress to LLM provider (Anthropic / OpenAI / local vLLM)

### 1.2 Required env vars (NEVER on argv)

| Variable | Source | Purpose |
|----|----|----|
| `HELIXON_LLM_KEY` | `op://Cursor_IronClaw/helixon-llm-key/credential` | LLM provider auth |
| `HELIXON_MEMORY_KEY` | `op://Cursor_IronClaw/helixon-memory-key/credential` | Engram memory L1 |
| `HELIXON_CONFIG_PATH` | env | Path to YAML config (default `/etc/helixon/config.yaml`) |

Always source via `op read ... | env -i HELIXON_LLM_KEY=$(cat) helixon serve` —
never `helixon serve --key=$HELIXON_LLM_KEY` (argv leak).

## 2. Install

### 2.1 Binary

```bash
# From release tarball (Linux x86_64)
curl -fsSL https://github.com/nfsarch33/helixon-platform/releases/download/v0.9.0/helixon_0.9.0_linux_amd64.tar.gz \
  | tar -xz -C /usr/local/bin helixon
chmod +x /usr/local/bin/helixon

# Verify
helixon version
```

### 2.2 Config

```bash
mkdir -p /etc/helixon
cat > /etc/helixon/config.yaml <<'YAML'
server:
  listen: ":8080"
  metrics_listen: ":9091"

llm:
  provider: anthropic  # or openai / openai-compatible
  model: claude-sonnet-4-7
  max_tokens: 4096

memory:
  l1_backend: engram
  l1_endpoint: "http://127.0.0.1:18888"
  l2_backend: git-kb
  l2_path: "/home/helixon/Code/cursor-global-kb"

channels:
  - type: http
    port: 8080
  - type: ws
    port: 8081
  - type: mcpstdio

agent:
  max_loop_iterations: 50
  tool_timeout_seconds: 30
  retry_max: 3
YAML
chmod 0600 /etc/helixon/config.yaml
```

### 2.3 systemd unit

```bash
cat > /etc/systemd/system/helixon.service <<'UNIT'
[Unit]
Description=Helixon Platform Agent Runtime
After=network.target

[Service]
Type=simple
User=helixon
EnvironmentFile=-/etc/helixon/env.conf
ExecStart=/usr/local/bin/helixon serve --config=/etc/helixon/config.yaml
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now helixon.service
systemctl status helixon.service
```

### 2.4 Health verify

```bash
sleep 5
curl -fsS http://localhost:8080/healthz | jq .
# {"status":"ok","uptime_seconds":5,...}

curl -fsS http://localhost:8080/readyz | jq .
# {"status":"ok","llm":"reachable","memory":"reachable"}
```

## 3. Upgrade

```bash
# 1. Stop the service
systemctl stop helixon.service

# 2. Backup current binary
cp /usr/local/bin/helixon /usr/local/bin/helixon.bak.$(date +%s)

# 3. Install new binary (from release tarball)
curl -fsSL https://github.com/nfsarch33/helixon-platform/releases/download/v0.9.1/helixon_0.9.1_linux_amd64.tar.gz \
  | tar -xz -C /usr/local/bin helixon
chmod +x /usr/local/bin/helixon

# 4. Verify version
helixon version

# 5. Run doctor before start
helixon doctor

# 6. Start
systemctl start helixon.service
systemctl status helixon.service

# 7. Verify
sleep 10
curl -fsS http://localhost:8080/healthz | jq .
```

If upgrade fails:

```bash
# Rollback to backup
cp /usr/local/bin/helixon.bak.<timestamp> /usr/local/bin/helixon
systemctl start helixon.service
```

## 4. K8s deployment

See `k8s/deployment.yaml` for the production manifest. The default
manifest uses:

- Image: `ghcr.io/nfsarch33/helixon-platform:0.9.0`
- Replicas: 2 (HA)
- Resources: 500m / 1Gi (requests), 1000m / 4Gi (limits)
- Liveness: `GET /healthz` on port 8080
- Readiness: `GET /readyz` on port 8080
- Memory backend: Engram via tunnel sidecar

```bash
kubectl apply -f k8s/
kubectl rollout status deployment/helixon
```

## 5. Backup / restore

### 5.1 What to backup

- Memory L1 DB: `~/.local/share/helixon/memory.db` (per-user)
- Memory L1 namespace config: `~/.config/helixon/memory.yaml`
- Logs: `~/logs/helixon/*.log` (optional, for forensics)

L2 (git KB) is already backed up via git remote.

### 5.2 Backup script

```bash
#!/bin/bash
# Daily backup of L1 + logs
BACKUP_DIR=/var/backups/helixon/$(date +%Y%m%d)
mkdir -p "$BACKUP_DIR"
sqlite3 ~/.local/share/helixon/memory.db ".backup '$BACKUP_DIR/memory.db'"
cp -r ~/.local/share/helixon/namespace "$BACKUP_DIR/"
tar -czf "$BACKUP_DIR-logs.tar.gz" ~/logs/helixon/
```

### 5.3 Restore

```bash
systemctl stop helixon.service
cp /var/backups/helixon/20260705/memory.db ~/.local/share/helixon/
cp -r /var/backups/helixon/20260705/namespace ~/.local/share/helixon/
systemctl start helixon.service
```

## 6. References

- [README.md](../README.md) — architecture
- [RUNBOOK.md](./RUNBOOK.md) — on-call procedures
- [k8s/deployment.yaml](../k8s/deployment.yaml) — K8s manifest
- `.gitlab-ci.yml` — CI/CD pipeline
- `.goreleaser.yaml` — release pipeline