# Sprint v14554 — wsl2/wsl3 k3s Join Scripts + GitLab CI Sentrux Pipeline

## Summary
Wired the k3s agent-join flow for the two outstanding fleet nodes (wsl2
and wsl3), and authored the canonical GitLab CI sentrux pipeline.

## Artefacts

### 1. k3s agent join scripts
- `scripts/k3s-agent-join.sh` — generic, idempotent join script for
  any fleet WSL host. Reads `K3S_URL` and `K3S_TOKEN` from the
  environment, stops any prior agent, purges `/etc/rancher/k3s` and
  `/var/lib/rancher/k3s`, then runs `curl | sh -` with the right
  `INSTALL_K3S_EXEC` arguments (node-name, node-labels,
  `--flannel-iface eth0`). Sets the `helixon.io/role=fleet-agent`
  label and `helixon.io/tier=secondary` tier.
- `scripts/k3s-agent-join-wsl3.sh` — wsl3-specific variant with
  `helixon.io/role=dev-planning` and `helixon.io/gpu=rtx5070ti`
  labels.

Both scripts require the operator to copy the cluster token from
wsl1 (`sudo cat /var/lib/rancher/k3s/server/node-token`) and the
server URL (`https://100.84.108.92:6443` — wsl1's Tailscale IP).

### 2. GitLab CI sentrux pipeline
- `ci/sentrux.gitlab-ci.yml` — 5-stage pipeline:
  1. **lint**: golangci-lint, govulncheck, semgrep
  2. **test**: `go test -race`, `go test -integration`
  3. **scan**: trivy fs, nancy, gitleaks
  4. **sentrux**: full sentrux audit (parses `sentrux-report.json`,
     fails on non-PASS verdict)
  5. **publish**: build+push image, apply ArgoCD manifests
- `ci/sentrux-config.yaml` — sentrux audit rules:
  - `no-direct-main-push` — deny
  - `no-plaintext-secrets` — deny
  - `registry-sot-required` — warn (every service must be in
    `registry.yaml`)
  - `llm-router-token-required` — deny
  - `engram-embed-url-required` — deny

### 3. Verification
- `python3 -c "yaml.safe_load(...)"` — both YAMLs parse clean.
- `wc -l` of the join scripts: `k3s-agent-join.sh` = 50 lines,
  `k3s-agent-join-wsl3.sh` = 30 lines.
- The join scripts are documented in the runbooks
  (`cursor-global-kb/sop/k3s-agent-join.md`) for operators.

## Notes
- The actual join on wsl2 and wsl3 was **not** performed in this
  sprint because we only have shell access to wsl1. Operators on
  win2/wsl2 and win3/wsl3 will run the scripts in v14555 once the
  observability dashboards are rolled out and the win2/wsl2 + win3/wsl3
  mesh is verified.
- The CI pipeline is committed but the GitLab Runner registration
  step (v14544 already created `scripts/install-gitlab-runner.sh`)
  still requires the GitLab CE URL+token, which will be added when the
  runner is enabled.