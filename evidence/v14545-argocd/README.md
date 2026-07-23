# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14545 Evidence — Argo CD install on k3s with app-of-apps sync

**Date**: 2026-07-09
**Sprint**: v14545 (Pair 3 Review)
**Status**: ✅ CLOSED — CF-v14536-02 resolved

## Acceptance criteria

- [x] Argo CD installed on k3s (desktop-12ro1af, wsl1)
- [x] Pre-created `argocd-redis` secret (CF-v14536-02)
- [x] All 5 Argo CD pods running (1/1) on desktop-12ro1af (central node)
- [x] App-of-apps synced (2 services: helixon-platform, helixon-sprintboard)
- [x] Argo CD UI reachable via NodePort 30080 (http) / 31423 (https)
- [x] Local git repo registered with git-daemon on port 9418

## Pod status

```text
NAME                                                READY   STATUS    RESTARTS   AGE
argocd-application-controller-0                     1/1     Running   0          ...
argocd-applicationset-controller-5b9675dd86-5ql4v   1/1     Running   0          ...
argocd-redis-b97648d6d-s2xsn                        1/1     Running   0          ...
argocd-repo-server-666d565b88-7lsxq                 1/1     Running   0          ...
argocd-server-5d78df69bf-9xlbr                      1/1     Running   0          ...
```

## App-of-apps status

```text
NAME                  SYNC STATUS   HEALTH STATUS   PROJECT
apps                  Synced        Healthy         helixon
helixon-platform      Synced        Healthy         helixon
helixon-sprintboard   Synced        Healthy         helixon
```

## Service access

| Service           | URL                              |
|-------------------|----------------------------------|
| Argo CD UI (HTTP) | http://127.0.0.1:30080           |
| Argo CD UI (HTTPS)| https://127.0.0.1:31423          |
| Argo CD API       | http://127.0.0.1:8081 (pf)       |
| Local git daemon  | git://100.84.108.92:9418/...     |

## Architecture decisions

1. **Node pinning**: All Argo CD pods pinned to wsl1 (desktop-12ro1af) via
   `nodeSelector` + `affinity.nodeAffinity.type=hard`. The chart's helper
   `argo-cd.affinity` reads `nodeAffinity.matchExpressions` only if
   `nodeAffinity.type: "hard"` is set.
2. **Redis secret**: Pre-created with name `argocd-redis` (NOT `argocd-redis-secret`).
   The chart's `redis.auth.existingSecret` references the secret name directly;
   the previous misname caused pods to crash with `couldn't find key auth`.
3. **CRDs**: Applied via `kubectl apply --server-side --validate=false`. The
   `applicationsets.argoproj.io` CRD has 1.3MB of annotations (above the
   256KB API server limit), so server-side apply is required.
4. **Helm hooks**: Disabled via `--no-hooks` (the chart's pre-install job hangs
   on the WSL2 kubelet `containerLog` endpoint).
5. **Local git**: `git daemon` on port 9418 serves a bare clone at
   `~/Code/git-mirrors/helixon-platform.git`. Pods reach it via the wsl1
   Tailscale IP (100.84.108.92). The git daemon runs as a keep-alive script
   in `~/.local/bin/gitd-keepalive.sh`.

## CF-v14536-02 closure notes

The pre-install hang was caused by:
1. Argo CD chart's pre-install job using `kubectl exec` into pods, which hangs
   against the WSL2 kubelet 10250 endpoint (returns 502).
2. Missing redis secret with the correct name (`argocd-redis` not
   `argocd-redis-secret`).
3. Missing CRDs at the time the application-controller tried to query them.
4. Anti-affinity rule overriding nodeSelector in the chart's helper.

All four issues addressed in this sprint.

## Files

- `k8s/argocd/values.yaml` — corrected values (nodeSelector, affinity, redis name)
- `k8s/argocd/apps.yaml` — app-of-apps manifest (AppProject + 3 Applications)
- `scripts/install-argocd.sh` — idempotent installer
- `local/bin/pf-argocd.sh` — port-forward keep-alive for CLI access
- `local/bin/gitd-keepalive.sh` — git daemon keep-alive

## Evidence files

- `pod-status.txt` — `kubectl get pods -n argocd`
- `app-status.txt` — `kubectl get applications -n argocd`
- `svc-status.txt` — `kubectl get svc -n argocd`
- `health-checks.txt` — HTTP healthz output
- `crds.txt` — `kubectl get crds | grep argoproj`