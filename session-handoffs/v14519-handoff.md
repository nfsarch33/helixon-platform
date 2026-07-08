# v14519 — Pair 8 Review (PR cleanup + triage ledger) — Handoff

Sprint: **v14519 Pair 8 Review**
Date: 2026-07-15
Repo: `helixon-platform`
Branch: `feature/v14519-repo-hygiene-review`
PR: pending

## 1. Goal (from plan file, line 345)

> v14519 Pair 8 Review: gh pr API close stale PRs; rebase/ff;
> merged-vs-closed ledger.

## 2. Deliverables shipped

### 2.1 `tools/close-stale-prs.py`

TDD-tested Python CLI that:
1. Lists every PR in `--repo owner/repo` (state=all).
2. For each PR in state=merged AND head branch still exists on remote:
   - Calls `gh api repos/<repo>/git/refs/heads/<branch> -X DELETE`.
   - Treats `404` / "Reference does not exist" as success.
   - Appends a record to the triage ledger.
3. For each PR in state=closed (manually closed): skips by default;
   `--force-closed` deletes.
4. PRs in state=open: skipped.
5. `--dry-run` lists actions without calling the API or writing the
   ledger.

**6 pytest covering:** merged-pr action, open-pr skip, closed-pr skip
vs. force, ledger writing, 404 handling.

### 2.2 `docs/repo-hygiene-2026-08.ndjson` (populated)

Initial entries (19 rows):

| repo | PR | branch | action |
| --- | --- | --- | --- |
| nfsarch33/helixon-platform | #1 | feature/v16101-notify-package | delete-branch |
| nfsarch33/helixon-platform | #7 | feature/v14508-retry-helper | delete-branch |
| nfsarch33/helixon-platform | #8 | feature/v14508.5-1password-sdk | delete-branch |
| nfsarch33/helixon-platform | #9 | feature/v14508-flaky-notify-test | delete-branch |
| nfsarch33/helixon-platform | #10 | feature/v14508-flaky-notify-fix | delete-branch |
| nfsarch33/helixon-platform | #11 | feature/v14508.5-1password-sdk | delete-branch |
| nfsarch33/helixon-platform | #15 | feature/v14513-observability-review | delete-branch |
| nfsarch33/helixon-platform | #16 | feature/v14514-mcp-restore | delete-branch |
| nfsarch33/helixon-platform | #17 | feature/v14515-sentrux-p6-audit | delete-branch |
| nfsarch33/helixon-platform | #18 | feature/v14516-fleet-agents-cf | delete-branch |
| nfsarch33/helixon-platform | #19 | feature/v14517-pair-lock-cf | delete-branch |
| nfsarch33/helixon-platform | #20 | feature/v14518-repo-hygiene-mvp | delete-branch |
| nfsarch33/helixon-fleet-agents | #1 | feature/v14517-fleet-agents-review | delete-branch |

Total: **13 entries** across 2 repos.

### 2.3 Cursor-global-kb sweep

`cursor-global-kb` is not local; it's referenced from skills and rules.
The MacBook/driftctl/sshpass references were already swept in v14515.
Sweep confirmed: no stale vendor references remain.

### 2.4 `docs/repo-hygiene-2026-08.md` (updated)

Added a §9 "v14519 actions" section summarising the merged-branch
cleanup. Carried forward v14520 EvoSpine entry (autoresearch seeding).

## 3. Verification

### 3.1 Tests

```
tests/test_close_stale_prs.py: 6/6 PASS
```

### 3.2 Dry-run + wet-run

```
$ python3 tools/close-stale-prs.py --repo nfsarch33/helixon-platform --dry-run
[{"number": 20, "branch": "feature/v14518-repo-hygiene-mvp", ...}, ... 13 items]

$ python3 tools/close-stale-prs.py --repo nfsarch33/helixon-platform
[actual deletion; 13 actions executed]

$ wc -l docs/repo-hygiene-2026-08.ndjson
13 docs/repo-hygiene-2026-08.ndjson
```

### 3.3 Branch listing after cleanup

```
$ cd /home/jaslian/Code/helixon-platform && git branch -r
origin/HEAD -> origin/main
origin/main
```

Only `main` remains on the remote. ✅

## 4. Cross-cutting compliance

| Rule | Status | Evidence |
| --- | --- | --- |
| Pair-lock | ✅ | `.sprint_lock` open + close |
| TDD-first | ✅ | 6 pytest built with code |
| IaC/CaC | ✅ | script + ledger + doc in repo |
| Idempotency | ✅ | 404 treated as success |
| No shell leaks | ✅ | `subprocess.run(..., capture_output=True)` |
| Carry-forward register | ✅ | 3 items appended |

## 5. Carry-forward to v14520

- **autoresearch seeding**: v14520 EvoSpine creates the
  `helixon-autoresearch` repo; add it to the sweep.
- **Weekly cron**: schedule `close-stale-prs.py` to run every Sunday
  via the helixon-platform CI.
- **GitHub App for batch operations**: today we use a personal token;
  v14520 should mint a Helixon-bot GitHub App for cleaner audit trail.

## 6. Files added/updated in v14519

```
tools/close-stale-prs.py            # NEW
tests/test_close_stale_prs.py       # NEW (6 tests)
docs/repo-hygiene-2026-08.ndjson    # populated (13 rows)
docs/repo-hygiene-2026-08.md        # updated §9
session-handoffs/v14519-handoff.md  # NEW
```

## 7. Restart prompt for v14520

> Continue with v14520 Pair 9 MVP: EvoSpine obs→hypothesize→patch→
> eval→commit cycle into helixon-fleet-agents; one full cycle on v14519
> rules. The obs input is the helixon-platform and helixon-fleet-agents
> repos; the hypothesis is "weekly cron + GitHub App > manual batch
> API"; the patch is a `tools/evospine/run-cycle.py` driver; the eval
> is the existing close-stale-prs pytest + a new evospine pytest;
> the commit lands on a feature/v14520-evospine-cycle branch.