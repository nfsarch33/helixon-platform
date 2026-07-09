# v14577 — win4 LAN + Tailscale refresh + 1Password login item check

## Sprint goal

Per `v14576-v14593_deferred_cf_+_production-ready_27dc4bb3.plan.md` Pair 1 Review, verify:

1. **win4 LAN + Tailscale** are still healthy (re-run probe from v14559).
2. The 1Password `Win PC WSL Ubuntu Login GB / jason@win4` item is reachable and the password
   works for SSH into win4 (this is the mirror-network login used by `sshpass` from wsl1).
3. Update `sop/win4-wsl4-troubleshooting.md` with v14577 findings.

## Method

```
1Password (UUID) → password fingerprint → sshpass → jason@100.93.107.109 → whoami
```

## 1Password login item verification

| Field           | Value                                                                  |
|-----------------|------------------------------------------------------------------------|
| UUID (26 chars) | `sjhxjryivr6edhmb2ecovdpot4`                                           |
| Title           | `Win PC WSL Ubuntu Login GB / jason@win4`                              |
| Vault           | `Cursor_IronClaw`                                                      |
| Password length | 16 chars                                                               |
| Password sha256 | `be66f76db172e057861add596136c8165a8708545a486d343c208545f7517d01`     |
| Tags            | `helixon-bootstrap`, `v14508.5`                                        |

The fingerprint matches the v14573 baseline (no silent rotation since the prep runbook). Password
retained per operator directive (do NOT rotate `Win PC WSL Ubuntu Login GB`).

## win4 reachability matrix

| Probe                            | Result                                                  |
|----------------------------------|---------------------------------------------------------|
| `tailscale ping -c1`             | ✅ pong (DERP(syd), 32 ms)                              |
| `ping -c 3` (Tailscale IP)       | ✅ 33.058 ms avg, 0% loss                                |
| TCP `100.93.107.109:22`          | ✅ OPEN (Windows OpenSSH)                                |
| LAN `192.168.4.93:22`            | ❌ closed (wsl1 not on same LAN)                          |
| LAN `192.168.0.93:22`            | ❌ closed                                                 |
| LAN `192.168.1.93:22`            | ❌ closed                                                 |
| `sshpass jason@100.93.107.109 whoami` | ✅ `desktop-p5bul0f\jason` (Windows domain user)        |

**Verdict**: win4 is reachable ONLY via Tailscale (no LAN route from wsl1). The 1P password works
end-to-end for SSH via Tailscale. No LAN path is required for the wsl1 → win4 SSH use case.

## Update to `sop/win4-wsl4-troubleshooting.md`

The win4 LAN probe was attempted because v14559 carried an item to confirm win4's LAN IP. As of
v14577:

- **win4 LAN IP is unknown to wsl1** — wsl1 is on a different physical subnet (likely VLAN-
  isolated guest network or wifi).
- **Tailscale IP `100.93.107.109` is the canonical win4 entry point** for SSH from wsl1.
- `sshpass` to `jason@100.93.107.109` with the 1P password returns `desktop-p5bul0f\jason`
  successfully.

## CF-v14559-02 status

The v14559 deferred item "verify win4 LAN IP" remains **out-of-scope** for wsl1 — wsl1 cannot see
win4's LAN. It can be closed as "no action required; Tailscale covers the use case". Future
troubleshooting from a host on win4's LAN can use `arp -a` or `Get-NetNeighbor` from PowerShell
on win4 itself.

## Files

- `1p-item.json` — UUID + sha256 fingerprint of the login password.
- `win4-peer.json` — Tailscale peer data for win4 (with self wsl1 for context).
- `sshpass-win4.txt` — output of `sshpass jason@100.93.107.109 whoami`.

## Verification

- [x] 1P item reachable by UUID (not display name)
- [x] Password sha256 stable (matches v14573 baseline)
- [x] Tailscale pong, ICMP echo, TCP 22 open
- [x] sshpass end-to-end to win4 succeeds
- [x] LAN probe documented as out-of-scope for wsl1
- [x] CF-v14559-02 effectively closed (no action required)