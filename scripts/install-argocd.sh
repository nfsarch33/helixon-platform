# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
#!/usr/bin/env bash
# install-argocd.sh — v14545 deploy (v2: fixed)
#
# Installs Argo CD on wsl1's k3s cluster with:
#   1. Pre-created redis secret (chart needs existingSecret: argocd-redis)
#   2. CRDs applied separately via kubectl (server-side to bypass 256KB limit)
#   3. --no-hooks to bypass WSL2 kubelet bug
#   4. Pinned nodeSelector to wsl1 (central node) via values.yaml
#
# Closes CF-v14536-02 (ArgoCD install hang on pre-install job).

set -euo pipefail

cd "$(dirname "$0")/.."  # cd to helixon-platform root
K8S_DIR="k8s/argocd"
DRY_RUN=0
HELM=/home/jaslian/local/bin/helm

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=1; shift ;;
        -h|--help)  sed -n '2,20p' "$0"; exit 0 ;;
        *)          echo "unknown flag $1"; exit 2 ;;
    esac
done

CHART_TGZ="/home/jaslian/.cache/helm/repository/argo-cd-10.1.2.tgz"

# Ensure namespace + redis secret
echo "=== 1. namespace + redis secret (correct name: argocd-redis) ==="
sudo k3s kubectl create namespace argocd --dry-run=client -o yaml | sudo k3s kubectl apply -f -

if ! sudo k3s kubectl -n argocd get secret argocd-redis >/dev/null 2>&1; then
    REDIS_PASS=$(openssl rand -hex 16)
    sudo k3s kubectl -n argocd create secret generic argocd-redis \
        --from-literal=auth="$REDIS_PASS"
    echo "$REDIS_PASS" > /tmp/argocd-redis-pass
    mkdir -p /home/jaslian/.cache/helixon
    echo "$REDIS_PASS" > /home/jaslian/.cache/helixon/argocd-redis-pass
    echo "redis secret created (pass saved to ~/.cache/helixon/argocd-redis-pass)"
else
    echo "redis secret already present"
fi

echo
echo "=== 2. apply Argo CD CRDs (server-side to bypass 256KB annotation limit) ==="
if [[ ! -s /tmp/argocd-crds-only.yaml ]]; then
    "$HELM" template argocd "$CHART_TGZ" \
        --namespace argocd \
        --values "$K8S_DIR/values.yaml" \
        --show-only templates/crds/crd-application.yaml \
        --show-only templates/crds/crd-applicationset.yaml \
        --show-only templates/crds/crd-appproject.yaml \
        --skip-tests --set crds.install=true > /tmp/argocd-crds-only.yaml
fi
if [[ $DRY_RUN -eq 1 ]]; then
    echo "(dry-run: not applying CRDs)"
    grep -E '^kind:|^  name:' /tmp/argocd-crds-only.yaml | head -20
else
    sudo k3s kubectl apply --server-side --validate=false \
        --field-manager=argocd-crds -f /tmp/argocd-crds-only.yaml
fi

echo
echo "=== 3. helm upgrade --install (--no-hooks to bypass WSL2 kubelet bug) ==="
if [[ $DRY_RUN -eq 1 ]]; then
    "$HELM" upgrade --install argocd "$CHART_TGZ" \
        --namespace argocd \
        -f "$K8S_DIR/values.yaml" \
        --no-hooks --dry-run 2>&1 | head -30
else
    "$HELM" upgrade --install argocd "$CHART_TGZ" \
        --namespace argocd \
        -f "$K8S_DIR/values.yaml" \
        --no-hooks --wait --timeout 5m
fi

echo
echo "=== 4. expose argocd-server via NodePort (30080 http, 31423 https) ==="
sudo k3s kubectl -n argocd patch svc argocd-server \
    --type=merge \
    -p '{"spec":{"type":"NodePort","ports":[{"name":"http","port":80,"targetPort":8080,"nodePort":30080},{"name":"https","port":443,"targetPort":8080,"nodePort":31423}]}}'

echo
echo "=== 5. verify ==="
sudo k3s kubectl -n argocd get pods,svc