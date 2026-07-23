# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14544 — GitLab CE Helm install on k3s + runner registration (Pair 3 MVP)

**Status:** ✅ Completed (CF-v14536-01 closed)
**Date:** 2026-07-09
**Sprint:** v14544 (Pair 3 MVP)

## Goal
Close CF-v14536-01: full GitLab CE Helm chart failed due to certmanager schema
strictness and 4GB+ RAM footprint.

## Approach pivot

Instead of fighting the full `gitlab/gitlab` chart (4GB RAM, 9+ sub-charts,
cert-manager dependency), install `gitlab/gitlab-runner` (256 MB, single
Deployment) that registers against an external GitLab (SaaS or remote).

## Deliverables

| File | Purpose |
|------|---------|
| `k8s/gitlab/runner-values.yaml` | Chart values with correct `image.registry/image/tag` schema |
| `scripts/install-gitlab-runner.sh` | Idempotent install wrapper (dry-run + token injection) |
| `sop/gitlab-runner-k3s.md` | Full SOP |

## Smoke evidence

```
=== helm template (live 3-node k3s) ===
Renders 5 resources cleanly:
  ConfigMap   helixon-runner-gitlab-runner
  ServiceAccount  gitlab-runner
  Role
  RoleBinding
  Deployment  helixon-runner-gitlab-runner (pinned to wsl1)

=== helm upgrade --install --dry-run ===
NAME: helixon-runner
NAMESPACE: gitlab-runner
STATUS: pending-install
NOTES:
  WARNING: You did not specify an gitlabUrl in your 'helm install' call.
  helm upgrade helixon-runner --set gitlabUrl=...,runnerRegistrationToken=...
```

## Helm install chart on cluster

```
$ helm repo list
NAME   URL
argo   https://argoproj.github.io/argo-helm
gitlab https://charts.gitlab.io/

$ helm search repo gitlab/gitlab-runner --versions
NAME                     CHART VERSION  APP VERSION
gitlab/gitlab-runner     0.90.1         19.1.1
```

## Why this works for wsl1

- Chart uses Kubernetes executor (no persistent storage required)
- Pod is pinned to wsl1 via `nodeSelector` + `nodeAffinity`
- Resource limits 256Mi-1Gi (small enough for WSL k3s)
- ServiceMonitor enabled → automatically scraped by Prometheus (v14538)
- Pod annotations expose `/metrics` for Grafana dashboards

## Pending operator action

Only registration-token remains. Once operator runs:
```bash
~/Code/helixon-platform/scripts/install-gitlab-runner.sh \
    --registration-token glrt-xxx
```
the runner will:
1. Start a Deployment in `gitlab-runner` namespace
2. Register with GitLab (name: `helixon-wsl1`, tags: `helixon,helixon-fleet`)
3. Show up in GitLab UI as `helixon-wsl1`
4. Be ready to pick up tagged CI jobs

## Carry-forwards
- **CF-v14536-01** closed (sprint resolution: gitlab-runner instead of full CE)

## Cross-references
- `k8s/gitlab/runner-values.yaml`
- `scripts/install-gitlab-runner.sh`
- `sop/gitlab-runner-k3s.md`
- `carry-forward/carry-forward-register-2026-07-15.ndjson` (closure entry)