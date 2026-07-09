# Sprint v14542 — win4/wsl4 LAN + Tailscale + k3s agent join

## Summary
Resolved the "no internet on wsl4" incident (CF-v14529-02) and made
the k3s-agent-join flow idempotent.

## Artefacts
- `cursor-global-kb/sop/win4-wsl4-troubleshooting.md`
- `helixon-platform/scripts/k3s-agent-join-wsl4.sh`

## Verification
- `tailscale status` shows wsl4 active
- `kubectl get nodes` shows wsl4 Ready
