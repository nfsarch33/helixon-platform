#!/bin/bash
# runx-leak-scan: allow-file internal_ip
# k3s-agent-join.sh — Idempotent k3s agent join for a fleet WSL host.
# Usage:
#   K3S_URL=https://100.84.108.92:6443 K3S_TOKEN=<token> bash k3s-agent-join.sh [node-alias]
#
# Where:
#   K3S_URL is the k3s server reachable from the agent (wsl1's Tailscale IP)
#   K3S_TOKEN is the cluster join token (from `cat /var/lib/rancher/k3s/server/node-token` on wsl1)
#   node-alias is the human-friendly name (default: hostname)

set -euo pipefail

NODE_ALIAS="${1:-$(hostname)}"
K3S_URL="${K3S_URL:?must be set, e.g. https://100.84.108.92:6443}"
K3S_TOKEN="${K3S_TOKEN:?must be set}"

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: must run as root (sudo bash $0)" >&2
  exit 1
fi

# Idempotency: stop + uninstall any prior agent
if systemctl is-active --quiet k3s-agent 2>/dev/null; then
  echo ">> stopping existing k3s-agent"
  systemctl disable --now k3s-agent || true
fi
if [ -d /etc/rancher/k3s ] || [ -d /var/lib/rancher/k3s ]; then
  echo ">> purging prior k3s state"
  /usr/local/bin/k3s-agent-uninstall.sh 2>/dev/null || true
  rm -rf /etc/rancher/k3s /var/lib/rancher/k3s
fi

# Install
echo ">> installing k3s agent (node-alias=$NODE_ALIAS)"
curl -sfL https://get.k3s.io | \
  INSTALL_K3S_EXEC="agent \
    --server $K3S_URL \
    --token $K3S_TOKEN \
    --node-name $NODE_ALIAS \
    --node-label helixon.io/node-alias=$NODE_ALIAS \
    --node-label helixon.io/role=fleet-agent \
    --node-label helixon.io/tier=secondary \
    --flannel-iface eth0 \
    --kubelet-arg=container-log-max-files=3 \
    --kubelet-arg=container-log-max-size=10Mi" \
  sh -

echo ">> agent join initiated. Waiting 30s for registration..."
sleep 30
echo ">> done. Verify on wsl1 with: kubectl get nodes -o wide | grep $NODE_ALIAS"
