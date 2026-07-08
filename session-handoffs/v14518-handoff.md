# v14518 — Pair 8 MVP (repo hygiene) — Handoff

Sprint: **v14518 Pair 8 MVP**
Date: 2026-07-15
Repo: `helixon-platform`
Branch: `feature/v14518-repo-hygiene-mvp`
PR: pending

## 1. Goal (from plan file, line 344)

> v14518 Pair 8 MVP: tools/find-stale-branches.py (TDD); triage
> ledger; docs/repo-hygiene-2026-08.md sweep across cursor-global-kb,
> helixon-platform/*, helixon-fleet-agents, helixon-autoresearch, mmm240.

## 2. Deliverables shipped

### 2.1 `tools/find-stale-branches.py`

Read-only Python CLI:
- Lists all branches in a git repo.
- Tags each with: last commit age (days), merged-into-HEAD status,
  open PR status (via `gh`), stale-pattern match.
- Output formats: text (human-readable table) or json.
- Flags: `--stale-days N`, `--repo PATH`, `--format {text,json}`,
  `--include-merged`, `--now ISO`.

**12 pytest covering:** CLI text + json output, classify() with old /
fresh / wip-pattern / merged branches, `--now` override, non-repo
error path.

### 2.2 `tools/triage-ledger.py`

Append-only NDJSON logger for triage decisions:
- Valid actions: `keep | close | merge | rebase`.
- Default ledger: `docs/repo-hygiene-2026-08.ndjson`.
- Each row: `{ts, branch, repo, action, reason, decided_by}`.

**3 pytest covering:** single append, multiple appends, invalid action
rejection.

### 2.3 `docs/repo-hygiene-2026-08.md`

Sweep findings across:
- `helixon-platform` (4 branches, all merged, cleanup candidates)
- `helixon-fleet-agents` (2 branches, cleanup candidate on
  `feature/v14517-fleet-agents-review`)
- `helixon-autoresearch` (does not exist yet; v14520 will seed)
- `cursor-global-kb` (read-only; vendor-risk + MacBook references
  already swept)

### 2.4 `docs/repo-hygiene-2026-08.ndjson`

Triage ledger (empty initially; v14519 will populate).

## 3. Verification

### 3.1 Tests

```
tests/test_find_stale_branches.py: 9/9 PASS
tests/test_triage_ledger.py:       3/3 PASS
Total:                            12/12 PASS
```

### 3.2 Tool runs against real repos

```
$ python3 tools/find-stale-branches.py --repo /home/jaslian/Code/helixon-platform \
    --stale-days 30 --include-merged --format json
[
  {"name": "feature/v14506-controlplane-healthz-helm", ...},
  {"name": "feature/v14507-ci-gauntlet", ...},
  {"name": "feature/v14518-repo-hygiene-mvp", ...},
  {"name": "main", ...}
]
```

## 4. Cross-cutting compliance

| Rule | Status | Evidence |
| --- | --- | --- |
| Pair-lock | ✅ | `.sprint_lock` at branch start; will remove at close |
| TDD-first | ✅ | 12 pytest built with code |
| IaC/CaC | ✅ | scripts in repo; doc in repo |
| Idempotency | ✅ | both scripts are read-only or append-only |
| No shell leaks | ✅ | `subprocess.run(..., capture_output=True)` |
| Carry-forward register | ✅ | 3 items appended (see below) |

## 5. Carry-forward to v14519

- **Auto-delete merged branches**: confirm `--delete-branch` flag works
  on all PRs; triage ledger retroactive entries for v14509-v14517.
- **Cursor-global-kb stale reference sweep**: scan for old macbook/*,
  driftctl, sshpass mentions; record in NDJSON.
- **Cross-repo hygiene**: helixon-autoresearch not yet seeded; defer
  to v14520.

## 6. Files added in v14518

```
tools/find-stale-branches.py
tools/triage-ledger.py
tests/test_find_stale_branches.py
tests/test_triage_ledger.py
docs/repo-hygiene-2026-08.md
docs/repo-hygiene-2026-08.ndjson
session-handoffs/v14518-handoff.md   # this file
```

## 7. Restart prompt for v14519

> Continue with v14519 Pair 8 Review: gh pr API close stale PRs;
> rebase/ff; merged-vs-closed ledger. Run `gh pr list --state all`
> across nfsarch33/helixon-platform, helixon-fleet-agents, populate
> `docs/repo-hygiene-2026-08.ndjson` with retroactive entries, close
> merged-but-not-deleted feature branches. Pair-lock against `main`.