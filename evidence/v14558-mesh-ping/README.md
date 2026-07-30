# runx-leak-scan: allow-file internal_ip
# Sprint v14558 — Mesh Reachability Sweep from wsl1

## Summary
Live sweep from wsl1 (`100.84.108.92`) to all 6 fleet Tailscale peers.
All peers REACHABLE, all DNS names resolve to the correct Tailscale IP.

## Matrix

| Peer | Tailscale IP | DNS Name | RTT (ms) | Status | DNS Match |
|------|--------------|----------|----------|--------|-----------|
| wsl1 | 100.84.108.92 | (self) | 0.0 | SELF | n/a |
| wsl2 | 100.110.82.5  | desktop-0s5prj9-1-wsl2.tail447712.ts.net | 37.3  | REACHABLE | YES |
| win2 | 100.100.103.106 | desktop-0s5prj9-win2.tail447712.ts.net    | 6.38  | REACHABLE | YES |
| win3 | 100.101.215.57  | laptop-qbf2fuls-win3.tail447712.ts.net    | 4.54  | REACHABLE | YES |
| wsl3 | 100.73.98.10   | laptop-qbf2fuls-wsl3.tail447712.ts.net    | 35.8  | REACHABLE | YES |
| win4 | 100.93.107.109 | desktop-p5bul0f-win4.tail447712.ts.net     | 32.6  | REACHABLE | YES |
| wsl4 | 100.79.227.40  | desktop-p5bul0f-wsl4.tail447712.ts.net     | 33.8  | REACHABLE | YES |

## Tailscale state per peer
- All 6 peers online; relay=syd (Sydney DERP), tx/rx counters active.
- win2 → `direct 192.168.4.64:41641` (LAN direct path)
- win3 → `direct 192.168.4.91:41641` (LAN direct path)
- wsl2/wsl4 → relay "syd" (no LAN direct because they're not on the same LAN as wsl1)
- win4 → relay "syd"
- win1/wsl1 (self) → no entry, this is the source

## DNS config note
Tailscale reports: "System DNS config not ideal. /etc/resolv.conf overwritten.
See https://tailscale.com/s/dns-fight". This is a known wsl1/wsl2
interop quirk; MagicDNS is functioning correctly (all 6 dns_match=TRUE).

## Artefacts
- `mesh-matrix.txt` — CSV version of the matrix
- `matrix.json` — full JSON with Tailscale state per peer
- `README.md` — this file

## Verification
- 6/6 fleet peers REACHABLE (100%)
- 6/6 MagicDNS names resolve to the correct Tailscale IP
- All peers are `online` per `tailscale status --json`
