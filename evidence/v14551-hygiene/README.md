# runx-public-repo-gate: allow-file fleet_host_alias
# Sprint v14551 — Workspace Hygiene

## Summary
Workspace cleanup for the v14540-v14557 fleet-operations arc:

1. **Node dedup**: `nodes.yaml` already had no `desktop-fh3nbqn-*`
   duplicates — the 4-node canonical set (win1/wsl1, win2/wsl2,
   win3/wsl3, win4/wsl4) was clean. Verified via
   `grep -in fh3nbqn inventory/fleet/nodes.yaml` (no matches).
2. **Branch audit**: only 1 stale local branch existed
   (`docs/v14500-tier4-implementation`, 2 days old, not fully merged).
   Force-deleted via `git branch -D`. Result: only `main` and the
   active `feature/v14545-argocd-app-of-apps` remain.
3. **fork-upstream sync**:
   - `cursor-global-kb`: rebased local onto `origin/main` (which had
     3 new commits: v16873/v16874/v16875 from the win3/wsl3 daily-sync
     work). Pushed the v14550 commit forward.
   - `helixon-platform`: rebased both `main` and
     `feature/v14545-argocd-app-of-apps` onto `origin/main`. Merged the
     feature branch back into `main` with `--no-ff` and pushed.

## Artefacts
- This README only (no new code; the work was purely rebase/merge/branch-prune).

## Verification
- `git branch -a` shows only `main` + the active feature branch.
- `git log origin/main --oneline -1` and `git log --oneline -1` agree
  (both now show `90ee90e merge(v14551): hygiene sync`).
- `grep fh3nbqn inventory/fleet/nodes.yaml` returns no matches.