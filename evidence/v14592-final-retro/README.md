# v14592 — Final 18-sprint retro + ADR-0098 + nodes.yaml dedupe

**Date**: 2026-07-10 (UTC+10)
**Sprint**: v14592 (Pair 9 MVP)

---

## TL;DR

- ✅ Wrote `sprint-retros/v14576-v14593-retro.md` (comprehensive 18-sprint summary).
- ✅ Created `cursor-global-kb/adrs/ADR-0098-win1-wsl1-production-readiness.md` (canonical production-ready topology).
- ✅ Audited `nodes.yaml` files for `desktop-fh3nbqn-*` duplicates — 0 active duplicates; clarified Windows hostname vs Tailscale hostname in wsl4 notes (v9 version-history entry).
- ✅ Updated `global-memories/fleet-hosts.md` to use current Tailscale hostnames for win4/wsl4.

---

## Dedupe audit (`nodes-dedupe-audit.json`)

| File | Active entries with fh3nbqn | Comment-only matches | Dedupe needed? |
|------|-----------------------------|---------------------|----------------|
| `cursor-global-kb/fleet/nodes.yaml` | 1 (Windows hostname `DESKTOP-FH3NBQN` in wsl4 notes — clarified in v14592) | 1 (v6 history comment) | No — hostname clarification only |
| `cursor-global-kb/inventory/fleet/nodes.yaml` | 0 | 0 | No |
| `helixon-kb/fleet/nodes.yaml` | 0 | 0 | No |

**Conclusion**: Per the v14502 (v6 version-history) dedupe, all `desktop-fh3nbqn-*` Tailscale hostnames were correctly renamed to `desktop-p5bul0f-*`. The only remaining reference is the **Windows hostname** `DESKTOP-FH3NBQN` (uppercase, Windows-side `hostname` output) in the wsl4 notes — this is the actual Windows host's computer name and is correct as-is (only clarified the relationship to the Tailscale hostname in v14592).

### Changes made in v14592

1. `cursor-global-kb/fleet/nodes.yaml` line 171: wsl4 notes now explain:
   > "WSL2 under Windows host DESKTOP-FH3NBQN (Tailscale hostname `desktop-p5bul0f-win4`, renamed v14502 from legacy `desktop-fh3nbqn-wsl4`)."

2. `cursor-global-kb/fleet/nodes.yaml` v9 history comment added.

3. `cursor-global-kb/global-memories/fleet-hosts.md` lines 28-29: Win4 and WSL4 rows updated to use `desktop-p5bul0f-*` and current Tailscale IPs (`100.93.107.109` and `100.79.227.40`).

---

## ADR-0098 — win1/wsl1 Production Readiness Topology

`cursor-global-kb/adrs/ADR-0098-win1-wsl1-production-readiness.md`

Captures the canonical production-ready state of win1/wsl1 after v14593:

- **6 fleet nodes**: win1/wsl1 (central), win2/wsl2, win3/wsl3, win4/wsl4, oracle-jump; MacBook RETIRED.
- **9 systemd user services + 5 timers** on wsl1 (engram, sprintboard-api, llm-router, svcregistryd, fleet-registrar, fleet-registrar-api, fleet-agent, alertmanager, ollama).
- **4-node k3s cluster** (wsl1 control plane + wsl2/wsl3/wsl4 agents).
- **Service registry**: 19 entries (static SOT + live daemon), bridged per ADR-0097 schema.
- **Observability stack**: Prometheus + Grafana + AlertManager + agentrace (all via Podman/systemd).
- **CI/CD**: GitHub → GitLab CE on wsl1 (`:8929`) → gitlab-runner → ArgoCD app-of-apps.
- **1Password vault**: 85 items total; 17 rotatable (cf. v14590), 2 hard-excluded, 3 soft-excluded (rotation deferred), 1 retired (delete pending).
- **9 carry-forwards** documented for next arc (CF-v14588-01 through CF-v14591-06).

### Boundaries

- OCI jump is single point of failure (ADR-017 DERP fallback).
- SprintBoard API currently unhealthy (CF-v14590-01).
- WSL login password not rotated (CF-v14591-03..06).
- 17 vendor keys inventoried but not yet rotated (CF-v14590-02).

---

## What was delivered

1. ✅ `sprint-retros/v14576-v14593-retro.md` — 18-sprint summary with closed CFs, new CFs, what worked/didn't, risk register, recommendations.
2. ✅ `adrs/ADR-0098-win1-wsl1-production-readiness.md` — canonical production-ready topology.
3. ✅ `cursor-global-kb/fleet/nodes.yaml` — v9 version comment + wsl4 notes clarification.
4. ✅ `cursor-global-kb/global-memories/fleet-hosts.md` — Win4/WSL4 rows updated.
5. ✅ `evidence/v14592-final-retro/nodes-dedupe-audit.json` — programmatic dedupe audit.

---

## References

- `sprint-retros/v14540-v14557-retro.md` (prior arc)
- `sprint-retros/v14558-v14575-retro.md` (prior arc)
- `adrs/ADR-0095`, `ADR-0096`, `ADR-0097` (related)
- `evidence/v14590-rotation/`, `evidence/v14591-rotation-arc/` (input from Pairs 7-8)
