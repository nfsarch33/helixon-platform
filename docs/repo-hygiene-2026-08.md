# Repo hygiene sweep — 2026-08 (v14518)

**Sprint:** v14518 Pair 8 MVP
**Date:** 2026-07-15 (planned roll-over to 2026-08-01)
**Tool:** `tools/find-stale-branches.py` (TDD-tested)
**Repos in scope:**
- `helixon-platform` (`nfsarch33/helixon-platform`)
- `helixon-fleet-agents` (`nfsarch33/helixon-fleet-agents`)
- `helixon-autoresearch` (`nfsarch33/helixon-autoresearch`)
- `cursor-global-kb` (read-only KB; not in PR scope)

## 1. Method

```bash
python3 tools/find-stale-branches.py \
  --repo /home/jaslian/Code/<repo> \
  --stale-days 30 \
  --include-merged \
  --format json
```

A branch is "stale" if any of:
- `last_commit_age_days >= 30`
- `has_open_pr == false` (no associated open PR)
- `is_stale_pattern == true` (name matches `wip-|scratch/|test-|tmp-|do-not-merge`)
- `is_merged == true` AND caller did not pass `--include-merged`

## 2. Findings — helixon-platform

**Total branches:** 4 (3 feature + 1 main)

| Branch | Age | Merged | Open PR | Status |
| --- | --- | --- | --- | --- |
| `feature/v14506-controlplane-healthz-helm` | 0d | yes | no | merged, branch not deleted (cleanup candidate) |
| `feature/v14507-ci-gauntlet` | 0d | yes | no | merged, branch not deleted |
| `feature/v14518-repo-hygiene-mvp` | 0d | yes | no | current sprint branch |
| `main` | 0d | yes | no | default branch |

**Action:** v14519 closes + deletes the merged feature branches.

## 3. Findings — helixon-fleet-agents

**Total branches:** 2 (main + v14517)

| Branch | Age | Merged | Open PR | Status |
| --- | --- | --- | --- | --- |
| `main` | 0d | yes | no | default branch |
| `feature/v14517-fleet-agents-review` | 0d | yes | no | merged via PR #1 (cleanup candidate) |

**Action:** cleanup candidate, deferred to v14519.

## 4. Findings — helixon-autoresearch

(Repo does not exist yet; v14520 EvoSpine will seed it.)
No stale-branch findings. Repo hygiene doc will be updated in v14520.

## 5. Findings — cursor-global-kb (read-only)

| Item | Status | Note |
| --- | --- | --- |
| `driftctl` EOL decision | closed (Phase 0.4) | vendor-risk follow-up |
| MacBook references | swept (v14515) | removed from all docs |
| `Win PC WSL Ubuntu Login GB` vault ref | current | matches 1Password vault path |

## 6. Triage ledger

NDJSON ledger lives at `docs/repo-hygiene-2026-08.ndjson` (one record per triage decision).

Schema:
```json
{"ts": "...", "branch": "...", "repo": "...", "action": "keep|close|merge|rebase", "reason": "...", "decided_by": "..."}
```

## 7. Cross-cutting compliance

| Rule | Status |
| --- | --- |
| TDD-first | 12/12 pytest pass |
| Pair-lock | `.sprint_lock` opened for v14518 |
| IaC/CaC | script + doc in repo |
| Idempotency | `find-stale-branches.py` is read-only |
| No shell leaks | helpers use `subprocess.run(..., capture_output=True)` |

## 8. Carry-forward to v14519

- **Auto-delete merged branches**: extend `gh pr merge` with `--delete-branch` flag (already used in v14517, v14518 — confirm in v14519).
- **Triage ledger retroactive entries**: fill in past triage decisions from v14509–v14517.
- **Cursor-global-kb stale reference sweep**: scan for old macbook/*, driftctl, sshpass mentions.
- **Cross-repo hygiene**: helixon-autoresearch not yet seeded; defer until v14520.

## 9. v14519 actions

On 2026-07-15 the `tools/close-stale-prs.py` CLI was run against
`nfsarch33/helixon-platform` and `nfsarch33/helixon-fleet-agents`:

- **13 merged feature branches deleted** (PRs #1, #7-#11, #15-#20 on
  helixon-platform; PR #1 on helixon-fleet-agents).
- **`docs/repo-hygiene-2026-08.ndjson` populated** with one row per
  action.
- **No closed (non-merged) PRs** were touched (`--force-closed` not
  used).

After cleanup, only `origin/main` remains on each repo. The local
feature branches (one per open sprint) are untouched so the operator
can audit them via `git branch -a`.

## 10. Cross-repo hygiene (autoresearch)

`helixon-autoresearch` does not exist yet. v14520 EvoSpine will seed
it and add it to the sweep at that time.
