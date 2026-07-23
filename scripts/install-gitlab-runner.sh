# runx-public-repo-gate: allow-file fleet_host_alias,personal_path_id
#!/usr/bin/env bash
# install-gitlab-runner.sh — v14544 deploy
#
# Installs gitlab-runner on the wsl1 k3s cluster using the chart values
# at k8s/gitlab/runner-values.yaml. Token is pulled from 1Password.
#
# Usage:
#   install-gitlab-runner.sh [--registration-token TOKEN] [--gitlab-url URL]
#
# Idempotent: helm upgrade --install is used. Safe to re-run.

set -euo pipefail

cd "$(dirname "$0")/.."  # cd to helixon-platform root
K8S_DIR="k8s/gitlab"

# Defaults (overridden via flags)
GITLAB_URL="https://gitlab.com/"
REG_TOKEN=""
DRY_RUN=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --registration-token) REG_TOKEN="$2"; shift 2 ;;
        --gitlab-url)         GITLAB_URL="$2"; shift 2 ;;
        --dry-run)            DRY_RUN=1; shift ;;
        -h|--help)            sed -n '2,20p' "$0"; exit 0 ;;
        *)                    echo "unknown flag $1"; exit 2 ;;
    esac
done

# Pull token from 1Password if not provided
if [[ -z "$REG_TOKEN" ]]; then
    if command -v op >/dev/null 2>&1; then
        # 1Password item: GitLab CE API PAT (wsl1) - reuse as registration token
        REG_TOKEN=$(op read "op://Cursor_IronClaw/GitLab CE API PAT (wsl1)/credential" 2>/dev/null || true)
    fi
    if [[ -z "$REG_TOKEN" ]]; then
        echo "ERROR: --registration-token not given and 1Password read failed"
        echo "Get one from your GitLab project: Settings > CI/CD > Runners > New project runner"
        exit 1
    fi
fi

# Replace placeholders in a temp copy of values
TMP_VALS=$(mktemp)
sed -e "s|https://gitlab.com/|$GITLAB_URL|" \
    -e "s|REGISTRATION_TOKEN_PLACEHOLDER|$REG_TOKEN|" \
    "$K8S_DIR/runner-values.yaml" > "$TMP_VALS"

echo "=== helm upgrade --install ==="
if [[ $DRY_RUN -eq 1 ]]; then
    echo "(dry-run)"
    helm upgrade --install helixon-runner gitlab/gitlab-runner \
        --namespace gitlab-runner \
        --create-namespace \
        --dry-run \
        -f "$TMP_VALS"
else
    helm upgrade --install helixon-runner gitlab/gitlab-runner \
        --namespace gitlab-runner \
        --create-namespace \
        -f "$TMP_VALS" \
        --wait
fi

rm -f "$TMP_VALS"

echo
echo "=== verify ==="
sudo k3s kubectl -n gitlab-runner get pods,svc,sa 2>&1 | head -20