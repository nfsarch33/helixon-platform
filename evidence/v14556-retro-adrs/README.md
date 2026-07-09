# Sprint v14556 — Final 18-sprint Retro + ADR-0095/0096 + Release Tag Prep

## Summary
Wrote the retro for the v14540-v14557 range, the two ADRs called out
in the plan, and pre-staged the release tag.

## Artefacts

### 1. Retro
- `helixon-platform/sprint-retros/v14540-v14557-retro.md` — one-line
  summary per sprint, KPI table (commits, new binaries, new units,
  new rules, new ADRs, new skills, new dashboards, new CI pipelines),
  wins, carry-forward items, and Sentrux readiness.

### 2. ADRs
- `cursor-global-kb/adrs/ADR-0095-helixon-service-registry.md` —
  documents the service registry architecture (registra, svcregistryd,
  svcregistry-bridge, helix-dev-tools services, browser UI,
  register-on-boot).
- `cursor-global-kb/adrs/ADR-0096-win4-recovery.md` — documents the
  win4/wsl4 Tailscale recovery procedure and the k3s-agent-join.sh
  idempotent recovery path.

### 3. Release tag prep
- Release tag `sentrux-2026-08-26` will be cut in v14557 after the
  final sentrux audit passes. Today (v14556) we prep:
  - All 16 prior sprint commits are pushed to `origin/main`.
  - All `evidence/v<NUMBER>-<slug>/README.md` exist and are committed.
  - No plaintext secrets in the working trees.
  - `git status --short` is clean.

## Verification
- `git log -1 --oneline` on cursor-global-kb → `48404ca7 docs(v14556): ADR-0095 + ADR-0096`.
- `git log -1 --oneline` on helixon-platform → `517db32 docs(v14556): final 18-sprint retro + release tag prep`.
- Both pushes succeeded (`To github.com:nfsarch33/... -> main`).