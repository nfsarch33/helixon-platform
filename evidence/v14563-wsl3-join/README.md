# runx-leak-scan: allow-file internal_ip
# Sprint v14563 — wsl3 k3s agent join + 4-node cluster diagram

## Summary
Joined wsl3 (laptop-qbf2fuls-wsl3) to the k3s cluster on wsl1 as
the `dev-planning` node (RTX 5070 Ti 12GB). The cluster is now
4 nodes total: wsl1 (control-plane) + wsl2 (fleet-agent) + wsl4
(llm-node) + wsl3 (dev-planning).

## Join sequence (highlights)
1. Copied `k3s-agent-join-wsl3.sh` to `/tmp/` on wsl3 via scp
2. Executed via sudo with `K3S_URL=https://100.84.108.92:6443`
3. First start failed (`bind: address already in use` on :6444)
4. Diagnosed stale k3s processes; killed with `pkill -9 -f k3s`
   and ran `/usr/local/bin/k3s-killall.sh`
5. Restarted `k3s-agent.service` — successful
6. Verified from wsl1: `wsl3 STATUS=Ready, AGE=4m`

## Final 4-node state

```
NAME              STATUS   ROLES           VERSION   INTERNAL-IP      OS-IMAGE
desktop-0s5prj9   Ready    <none>          v1.36.2   192.168.4.64     Ubuntu 24.04.4
desktop-12ro1af   Ready    control-plane   v1.36.2   172.29.144.56    Ubuntu 24.04.4
desktop-p5bul0f   Ready    <none>          v1.36.2   100.79.227.40    Ubuntu 24.04.4
wsl3              Ready    <none>          v1.36.2   172.20.113.115   Ubuntu 24.04.4
```

## Helixon labels applied
- wsl3 `helixon.io/role=dev-planning` ✓ (already set by join script)
- wsl3 `helixon.io/gpu=rtx5070ti` ✓ (already set by join script)
- wsl3 `helixon.io/node-alias=wsl3` ✓ (already set by join script)
- wsl3 `helixon.io/tier=primary` (newly added)

## Artefacts
- `join-output.txt` — original join attempt
- `cleanup-output.txt` — stale-process cleanup
- `debug-output.txt` — journal + status snapshot
- `labels-output.txt` — label application
- `cluster-diagram.md` — Mermaid diagram of the 4-node cluster
- `README.md` — this file

## Vendor verification
- k3s binary pulled from official `k3s-io/k3s` GitHub releases.
- sha256sum-amd64.txt verified against the binary.
- No typosquat risk; installer URL matches `https://get.k3s.io`.

## Verification
- 4/4 nodes Ready
- 4/4 nodes labelled with helixon.io/* keys
- wsl3 connects to control-plane via Tailscale
