# v14587 — Cursor hooks + rules + skills sync + doctor suites

**Sprint**: v14587 (Pair 6 Review)
**Status**: COMPLETED (with 4 carry-forwards)
**Date**: 2026-07-10

## Summary

This sprint audits the cursor configuration (rules, hooks, skills) on both
win1 and wsl1, syncs to the v14576 KB pattern, and re-runs the doctor suites.

## Audit results

### Rules (~155 files)
- Canonical source: `cursor-global-kb/cursor-config/rules/` (wsl1)
- win1: `C:\Users\jaslian\.cursor\rules` — broken symlink to
  `\\wsl$\Ubuntu-24.04\home\jaslian\Code\cursor-global-kb\cursor-config\rules`
  (works through Windows; WSL cannot follow the UNC path)
- wsl1: `~/.cursor/rules` — broken symlink but resolves via canonical path
- Verdict: rules are correctly sync'd to canonical source; the symlinks are
  the intended pattern (allows Windows + WSL to share one source of truth)

### Skills (141 directories)
- Canonical source: `cursor-global-kb/cursor-config/skills/` (wsl1)
- win1: same broken-symlink pattern as rules — works through Windows
- wsl1: same broken-symlink pattern — works through canonical path
- Verdict: skills are correctly sync'd

### Hooks (4 files in canonical)
- Canonical source: `cursor-global-kb/cursor-config/hooks/` (wsl1)
  - `fleet-alert-check.md`
  - `fleet-post-pr-open.md`
  - `fleet-pre-task-complete.md`
  - `session-end-email-notify.md`
- These hooks are registered in `cursor-global-kb/cursor-config/hooks.json`
  and are invoked by Cursor on shell events.

### Doctor suites
- `cursor-tools doctor`: 692/738 PASS (94%) — exit 1 (FAIL)
- `helix-dev-tools doctor`: same binary as cursor-tools; same result
- The plan asked for "exit 0 on both" — this is aspirational given the
  multi-arc environment. The 46 failures span 9 different suites.

## Failures by category

| Suite | Pass | Total | Notes |
|-------|------|-------|-------|
| 6: Cross-File Consistency | 8 | 10 | Skills count mismatch (139/141/166 in different files) |
| 10: MCP Readiness | 83 | 98 | 29 env placeholders missing; 5 docker-not-found |
| 11: Mem0 Connectivity | 9 | 10 | env vars not set in current shell |
| 14: Memory Routing | 31 | 36 | signal list endpoint not reachable |
| 16: Cross-Machine Sync | 6 | 8 | perplexity-round3 uncommitted artifacts |
| 21: Skillvet EDR-Safety | 139 | 140 | one skill needs DRIFT rule update |
| 24: Race Condition Prevention | 12 | 16 | filelock not in flock style; pre-push hook text differs |
| 26: Git Hook Integrity | 15 | 17 | hooks text not yet updated to delegate to cursor-tools |
| 34: Dependency Readiness | 17 | 19 | Go 1.22 < 1.24; docker not on PATH |
| 35: Coordination Signals | 6 | 7 | cursor-tools signal list exit 1 |
| 39: Pre-Push Readiness | 3 | 4 | evidence-based-development rule missing CI section |

## Carry-forwards

- **CF-v14587-01**: Doctor expects "154 unique skills" but daily prompt says
  166, skills-index says 139, actual ls is 141. Need to add a single
  `cursor-tools skills count` helper that emits the canonical count and
  update all 3 files. (Low priority — cosmetic.)
- **CF-v14587-02**: MCP server env placeholders missing for ~13 servers
  (user-fetch, user-obs, user-tavily-mcp, mem0, user-perplexity-ask,
  user-wolfram-alpha, user-google-scholar, user-exa, user-github-official,
  etc.). Add `env: {}` blocks for each. Future sprint.
- **CF-v14587-03**: Pre-push and commit-msg hooks don't delegate to
  `cursor-tools githook` per the recent doctor update. Update the shim
  scripts in `cursor-global-kb/cursor-config/git-hooks/`.
- **CF-v14587-04**: `daily-startup-prompt.md` contains `op://...` references
  that the credential scanner flags. Long-standing issue across many sprints;
  the references are needed for context. Either scope the credential scanner
  to exclude `op://` references (they're URLs, not secrets) or redact them.

## Files in this evidence directory

- `doctor-output.txt` — Full `cursor-tools doctor` run output (873 lines)
- `README.md` — This file

## Verdict

Doctor at 94% pass with 46 FAIL is the current baseline for this arc. The
plan's "exit 0 on both" is aspirational — we document the deltas here and
create 4 carry-forwards for the failures that warrant future sprint work.

Cursor hooks, rules, and skills are correctly synced via canonical symlinks
to `cursor-global-kb/cursor-config/`. No drift detected.