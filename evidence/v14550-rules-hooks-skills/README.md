# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# Sprint v14550 — Cursor Rules Sweep + Hooks Sync

## Summary
- Added 6 new fleet-specific rules to `cursor-global-kb/cursor-config/rules/`
  (priority prefix `01-` so they apply first):
  - `01-svcregistry-canon.mdc` — registry as the SOT, never hard-code ports.
  - `01-systemd-user-convention.mdc` — single template for all `*.service`
    units, secrets-bootstrap in `ExecStartPre`, `%h`-based paths.
  - `01-1password-uuid-required.mdc` — use 26-char UUIDs not display names
    (postmortem of v14546's `op read` parsing failure).
  - `01-fleet-doctor-cadence.mdc` — 6h timer + GREEN-gate convention.
  - `01-sprint-evidence-required.mdc` — `evidence/v<NUMBER>-<slug>/` pattern.
  - `01-tailscale-wsl-mirror.mdc` — wsl1 mirror-network constraints, the
    wsl1 → win1 forbidden route, and the git://9418 ArgoCD workaround.
- Rewrote `cursor-config/hooks.json` paths from legacy `/home/jason` and
  `~/Code/global-kb` to `/home/jaslian` and `~/Code/cursor-global-kb` so
  hooks resolve correctly on win1/wsl1.

## Artefacts
- `cursor-config/hooks.json` (modified)
- 6 new files under `cursor-config/rules/` (see git log `12f63850`)

## Verification
- `git log -1 --oneline` → `12f63850 feat(v14550): cursor rules sweep + hooks.json path sync`
- `git diff --cached --stat` → 7 files, 262 insertions(+), 9 deletions(-)
- Total rules in `cursor-config/rules/`: well over 15 (was already >100 from
  prior KB merges; the 6 new ones are fleet-specific canon rules).