# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# Sprint v14564 — ArgoCD app-of-apps re-sync on 4-node cluster

## Summary
After wsl3 joined the cluster in v14563, triggered a hard re-sync
of the `apps` Application (app-of-apps) to reconcile against the
new node topology. All 3 Applications (`apps`,
`helixon-platform`, `helixon-sprintboard`) remain Synced+Healthy.

## Re-sync trigger
```
kubectl -n argocd patch application apps --type merge \
  -p '{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}'
application.argoproj.io/apps patched
```

## App sync status (post-refresh)
```
NAME                  SYNC     HEALTH    REVISION
apps                  Synced   Healthy   a4747487ac2f2ca789796a6b841e5dfde6704c4f
helixon-platform      Synced   Healthy   a4747487ac2f2ca789796a6b841e5dfde6704c4f
helixon-sprintboard   Synced   Healthy   a4747487ac2f2ca789796a6b841e5dfde6704c4f
```

## ArgoCD pods (all 5/5 Ready)
```
argocd-application-controller-0                     1/1 Running   6h10m
argocd-applicationset-controller-5b9675dd86-5ql4v   1/1 Running   6h10m
argocd-redis-b97648d6d-s2xsn                        1/1 Running   6h10m
argocd-repo-server-666d565b88-7lsxq                 1/1 Running   6h10m
argocd-server-5d78df69bf-9xlbr                      1/1 Running   6h10m
```

## Cluster state (4 nodes, all Ready)
| Node | Role | Tier | GPU |
|------|------|------|-----|
| desktop-12ro1af (wsl1) | control-plane | primary | (none) |
| desktop-0s5prj9 (wsl2) | fleet-agent   | secondary | (none) |
| desktop-p5bul0f (wsl4) | llm-node      | primary | rtx3090 |
| wsl3                | dev-planning  | primary | rtx5070ti |

## Source repo
ArgoCD fetches manifests from `git://100.84.108.92:9418/helixon-platform.git`
(served by `git-daemon` on wsl1, port 9418).

## Artefacts
- `argocd-resync.txt` — full kubectl output + apps.yaml
- `README.md` — this file

## Verification
- 3/3 Applications Synced
- 3/3 Applications Healthy
- 5/5 ArgoCD pods Running
- 4/4 cluster nodes Ready
