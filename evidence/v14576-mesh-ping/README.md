# v14576 — Mesh reachability sweep (full Tailscale list)

## Sprint goal

Per the `v14576-v14593_deferred_cf_+_production-ready_27dc4bb3.plan.md` Pair 1 MVP, perform a
reachability sweep across the canonical 13-node Tailscale list (wsl1 self + 10 other peers) and
emit a structured matrix of `tailscale ping`, ICMP RTT, and MagicDNS resolution.

## Source of truth

Source node: **wsl1** (`desktop-12ro1af-wsl1`, Tailscale IP `100.84.108.92`, MagicDNS suffix
`tail447712.ts.net`).

## Reachability matrix (10 peers; wsl1 self excluded)

| Alias            | Tailscale IP      | Host                          | OS      | ts_pong | ts_path                | ICMP | RTT avg   | DNS |
|------------------|-------------------|-------------------------------|---------|---------|------------------------|------|-----------|-----|
| win1             | 100.119.60.6      | desktop-12ro1af-win1          | windows | ✅      | 192.168.4.92:41641 (direct LAN-punch) | ✅ | 0.999 ms  | ✅ |
| wsl2             | 100.110.82.5      | desktop-0s5prj9-1-wsl2        | linux   | ✅      | DERP(syd)              | ✅   | 54.244 ms | ✅ |
| win2             | 100.100.103.106   | desktop-0s5prj9-win2          | windows | ✅      | 100.100.57.26:37922 (LAN-punch) | ✅ | 24.388 ms | ✅ |
| wsl3             | 100.73.98.10      | laptop-qbf2fuls-wsl3          | linux   | ✅      | DERP(syd)              | ✅   | 37.575 ms | ✅ |
| win3             | 100.101.215.57    | laptop-qbf2fuls-win3          | windows | ✅      | 192.168.4.91:41641 (direct) | ✅  | 2.413 ms  | ✅ |
| wsl4             | 100.79.227.40     | desktop-p5bul0f-wsl4          | linux   | ✅      | DERP(syd)              | ✅   | 33.095 ms | ✅ |
| win4             | 100.93.107.109    | desktop-p5bul0f-win4          | windows | ✅      | DERP(syd)              | ✅   | 35.066 ms | ✅ |
| oci-sydney-jump  | 100.124.160.73    | oci-sydney-jump               | linux   | ❌      | (timed out)            | ❌   | n/a       | ✅ (DNS) |
| oracle-jump      | 100.93.242.75     | oracle-jump                   | linux   | ✅      | 159.13.40.202:41641 (direct) | ✅ | 18.039 ms | ✅ |
| players-aerq61a  | 100.79.23.118     | players-aerq61a               | windows | ❌      | (timed out, retired)   | ❌   | n/a       | ✅ (DNS) |

**Tailscale ping reachability**: 8/10 (excluding self).
**ICMP reachability**: 8/10.
**MagicDNS resolution**: 10/10.

## Findings & remediation

1. **win3 Tailscale IP recovered**: operator directive's table listed `lookup-ts` instead of an
   explicit IP. Resolved via `getent hosts laptop-qbf2fuls-win3` → `100.101.215.57` and confirmed
   with direct NAT-PMP LAN-punch RTT 2.4 ms. **No action required**, but flagged in the runbook.
2. **oci-sydney-jump timed out** — `tailscale status` reports
   `active; relay "syd"; offline, last seen 64d ago`. DNS still resolves but the host is offline.
   → **Carry-forward to v14588** (OCI jump refresh via Oracle web UI).
3. **players-aerq61a is RETIRED** — confirmed `offline, last seen 53d ago`. Expected; no action.
4. **/etc/resolv.conf warning** — Tailscale notes "System DNS config not ideal" (DNS fight).
   Cosmetic, doesn't affect resolution.
5. **Direct NAT-PMP LAN-punch** works for win1 (0.999 ms), win2 (24 ms via 100.100.57.26),
   win3 (2.4 ms), and oracle-jump (18 ms). The others fall back to DERP(syd).

## Files

- `matrix.json` — structured JSON (full reachability, paths, RTT, DNS).
- `tailscale-status.txt` — human-readable `tailscale status` output (15 peers incl. iphone172,
  jasnas3001, msi retired).
- `tailscale-status.json` — full `tailscale status --json`.
- `ping-raw.txt` — raw `tailscale ping -c 1` + `ping -c 3` output per node.
- `dns-raw.txt` — `getent hosts` per node.

## Verification

- [x] All 10 peer Tailscale IPs attempted
- [x] All 10 MagicDNS names resolve
- [x] 8/10 ts-ping pong
- [x] 8/10 ICMP echo reply
- [x] oci-sydney-jump + players-aerq61a failures recorded with reason
- [x] matrix.json schema: `alias / tailscale_ip / host / os / tailscale_pong / tailscale_path / icmp_reachable / icmp_rtt_avg_ms / dns_resolves`