# runx-public-repo-gate: allow-file fleet_host_alias
#!/bin/bash
# k3s-agent-join-wsl3.sh — wsl3-specific join (dev-planning role)
# Usage:
#   K3S_URL=https://100.84.108.92:6443 K3S_TOKEN=<token> bash k3s-agent-join-wsl3.sh
set -euo pipefail

NODE_ALIAS="${1:-wsl3}"
K3S_URL="${K3S_URL:?must be set}"
K3S_TOKEN="${K3S_TOKEN:?must be set}"

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: must run as root" >&2
  exit 1
fi

if systemctl is-active --quiet k3s-agent 2>/dev/null; then
  systemctl disable --now k3s-agent || true
fi
/usr/local/bin/k3s-agent-uninstall.sh 2>/dev/null || true
rm -rf /etc/rancher/k3s /var/lib/rancher/k3s

curl -sfL https://get.k3s.io | \
  INSTALL_K3S_EXEC="agent \
    --server $K3S_URL \
    --token $K3S_TOKEN \
    --node-name $NODE_ALIAS \
    --node-label helixon.io/node-alias=$NODE_ALIAS \
    --node-label helixon.io/role=dev-planning \
    --node-label helixon.io/gpu=rtx5070ti \
    --flannel-iface eth0" \
  sh -

echo ">> wsl3 joined as $NODE_ALIAS. Verify: kubectl get nodes -o wide | grep $NODE_ALIAS"
