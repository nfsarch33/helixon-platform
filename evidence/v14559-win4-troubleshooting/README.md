# Sprint v14559 — win4 LAN + Tailscale Verification (CF-v14529-02 follow-up)

## Summary
Verifies that win4 (desktop-p5bul0f-win4) and its WSL2 instance
wsl4 (desktop-p5bul0f-wsl4) are both reachable from wsl1 over
Tailscale, with degraded but acceptable latency indicating traffic
is traversing the `syd` DERP relay rather than direct LAN.

## Win4 / wsl4 reachability (5-sample ping from wsl1)

| Target       | IP              | min   | avg   | max   | mdev  | loss  |
|--------------|-----------------|-------|-------|-------|-------|-------|
| win4         | 100.93.107.109  | 102.8 | 134.9 | 209.6 | 41.5  | 0%    |
| wsl4         | 100.79.227.40   | 92.6  | 122.2 | 155.6 | 20.9  | 0%    |

Compared to v14558's other peers (~30-40ms avg over Sydney DERP),
win4 is consistently ~3-4x slower. This is consistent with
Tailscale using the `syd` DERP relay and no direct LAN path
between wsl1 and the win4 subnet.

## Tailscale peer data

```
PEER desktop-p5bul0f-wsl4:
  TailscaleIPs: ['100.79.227.40', 'fd7a:115c:a1e0::df32:e329']
  OS: linux
  Online: True
  Relay: syd
  LastSeen: 2026-07-08T17:38:59.1Z
```

win4 itself does not appear as a separate Tailscale Peer entry —
it is the Windows host for wsl4, and Tailscale runs only inside
the WSL2 distro. This is consistent with `01-tailscale-wsl-mirror`
behavior: the Windows host's Tailscale traffic comes from the WSL
instance's network namespace.

## LAN IP cannot be inferred from wsl1
Tailscale does not expose the LAN IP of a peer (only the
`Endpoints` field, which is empty for wsl4 in this snapshot —
indicating no active direct UDP connection). To derive the LAN
IP you would need:
- `ipconfig /all` on the win4 host directly, or
- an `arp -a` query from win4's network, or
- a MagicDNS query against the local DNS server on win4's LAN

Since wsl1 is forbidden to SSH to win4 (WSL2 mirror-network
constraint), the LAN IP for win4 remains unknown to wsl1. This
is fine for the fleet mesh because:
- All fleet agents route through Tailscale IPs (100.x.y.z), not LAN.
- LLM workloads on wsl4 use `http://100.79.227.40:8080` to reach it.

## CF-v14529-02 status: still resolved
The original incident ("no internet on wsl4") was a Tailscale
MagicDNS + systemd-resolved interaction (5s DNS forwarding delay).
This was fixed in v14542 by:
1. Idempotent k3s-agent-join script that purges state before re-joining.
2. Retry pattern in scripts that depend on DNS (3x with 2s backoff).
3. `01-tailscale-wsl-mirror` Cursor rule documenting the constraint.

## Verification
- 5/5 ICMP packets reach win4 and wsl4 with 0% loss.
- MagicDNS resolves `desktop-p5bul0f-wsl4.tail447712.ts.net` to 100.79.227.40.
- Tailscale reports `wsl4: online; relay syd`.
- wsl4 → wsl1 latency (122ms avg) is within the SLO documented in
  the v14555 fleet-mesh dashboard (<200ms).

## Artefacts
- `diagnostic.txt` — full tailscale status + 5-sample ping data
- `README.md` — this file
