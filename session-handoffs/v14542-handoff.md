# v14542 — host-d/node-d LAN + Tailscale troubleshooting + k3s agent join (Pair 2 MVP)

**Status:** ✅ Completed (closes CF-v14529-02)
**Date:** 2026-07-09
**Sprint:** v14542 (Pair 2 MVP)

## Goal
Resolve the long-standing carry-forward CF-v14529-02: node-d was reported as
having "no internet" and could not join the k3s cluster.

## Findings

### Diagnosis: NOT no internet — DNS-only
- `ping 1.1.1.1` → ✅ 7-8 ms (raw internet works)
- `getent hosts get.k3s.io` → ✅ resolves via systemd-resolved forwarding
- `curl https://get.k3s.io` → ✅ 200 OK
- `curl https://api.github.com` → ✅ 200 OK
- `systemctl is-active systemd-resolved` → active
- Tailscale relays in use (syd)

### Root cause
Earlier failure was transient — likely the Tailscale relay was still warming up.
Once Tailscale handshake completes, systemd-resolved forwards non-tailnet
queries upstream and everything works.

### Important: don't edit /etc/resolv.conf
Tailscale overwrites `/etc/resolv.conf` on every handshake. Use
`/etc/systemd/resolved.conf.d/` drop-ins instead.

## Actions

1. ✅ Probed host-d + node-d connectivity via SSH (LAN + Tailscale)
2. ✅ Verified DNS resolves via systemd-resolved (Tailscale Magic DNS)
3. ✅ Joined k3s agent on node-d (idempotent script with state cleanup)
4. ✅ Verified cluster membership

## k3s cluster state (3 nodes)

```
NAME              STATUS   ROLES           AGE     VERSION        INTERNAL-IP     EXTERNAL-IP
fleet-host-b   Ready    <none>          3h18m   v1.36.2+k3s1   192.0.2.64    100.64.0.3
fleet-host-a   Ready    control-plane   3h50m   v1.36.2+k3s1   172.29.144.56   <none>
fleet-host-d   Ready    <none>          27s     v1.36.2+k3s1   100.64.0.3   100.64.0.3
```

## Deliverables

- `sop/host-d-node-d-troubleshooting.md` — full troubleshooting guide + k3s agent
  join SOP for future reference

## Carry-forwards
- None. CF-v14529-02 is closed.

## Cross-references
- `inventory/fleet/nodes.yaml` — node-d entry
- `~/.ssh/config.d/fleet.conf` — node-d/host-d SSH aliases
- `sop/host-d-node-d-troubleshooting.md` — full guide
- `k8s/k3s/agent-join.sh` (v14529) — generic agent-join