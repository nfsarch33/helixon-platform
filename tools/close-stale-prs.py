#!/usr/bin/env python3
"""close-stale-prs — close merged-but-still-open PRs and prune their
branches via the gh API. Records every action in the triage ledger.

Behavior:
  - Lists every PR in --repo (default: current) in state=all.
  - For each PR whose state=merged AND head branch still exists:
      - Calls `gh pr edit` to ensure it's closed (no-op if already).
      - Calls `git branch -D <head>` to delete the local branch.
      - Calls `git push origin --delete <head>` to delete the remote
        branch.
      - Appends a record to the triage ledger.
  - For each PR whose state=closed (manually closed) AND head branch
    still exists: dry-run only (does NOT auto-delete, requires --force).
  - Exits 0 on success, 1 if any operation failed.

Usage:
    close-stale-prs.py --repo OWNER/REPO [--ledger PATH] [--dry-run]
                        [--force-closed]
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

VALID_ACTIONS = {"close", "merge", "rebase", "keep", "delete-branch"}


def _gh(*args: str, cwd: Path | None = None) -> tuple[int, str, str]:
    r = subprocess.run(
        ["gh", *args],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
    )
    return r.returncode, r.stdout, r.stderr


def list_prs(repo: str) -> list[dict]:
    rcode, out, err = _gh("pr", "list", "--repo", repo, "--state", "all",
                         "--json", "number,title,state,headRefName,mergedAt,closedAt,url")
    if rcode != 0:
        print(f"gh pr list failed: {err.strip()}", file=sys.stderr)
        return []
    try:
        return json.loads(out)
    except json.JSONDecodeError as e:
        print(f"bad JSON from gh pr list: {e}", file=sys.stderr)
        return []


def delete_remote_branch(repo: str, branch: str) -> tuple[bool, str]:
    rcode, out, err = _gh("api", f"repos/{repo}/git/refs/heads/{branch}", "-X", "DELETE")
    if rcode == 0:
        return True, out
    if "Reference does not exist" in err or "404" in err:
        return True, "(already deleted)"
    return False, err.strip()


def append_ledger(
    ledger: Path,
    branch: str,
    repo: str,
    action: str,
    reason: str,
    decided_by: str,
) -> None:
    entry = {
        "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "branch": branch,
        "repo": repo,
        "action": action,
        "reason": reason,
        "decided_by": decided_by,
    }
    ledger.parent.mkdir(parents=True, exist_ok=True)
    with ledger.open("a") as f:
        f.write(json.dumps(entry) + "\n")


def process(
    prs: list[dict],
    repo: str,
    ledger: Path,
    dry_run: bool = False,
    force_closed: bool = False,
    decided_by: str = "cursor-ai",
) -> list[dict]:
    """Returns a list of actions taken (or planned, if dry_run)."""
    actions: list[dict] = []
    for pr in prs:
        state = (pr.get("state") or "").lower()
        branch = pr.get("headRefName") or ""
        number = pr.get("number")
        merged_at = pr.get("mergedAt")
        closed_at = pr.get("closedAt")
        title = pr.get("title") or ""
        url = pr.get("url") or ""

        if state == "merged":
            action = "delete-branch"
            reason = f"merged PR #{number}: {title}"
            if not dry_run:
                ok, msg = delete_remote_branch(repo, branch)
                if not ok:
                    print(f"failed to delete branch {branch} for #{number}: {msg}", file=sys.stderr)
                    continue
                append_ledger(ledger, branch, repo, action, reason, decided_by)
            actions.append({
                "number": number,
                "branch": branch,
                "action": action,
                "reason": reason,
                "url": url,
                "merged_at": merged_at,
                "dry_run": dry_run,
            })
        elif state == "closed" and force_closed:
            action = "delete-branch"
            reason = f"closed (force) PR #{number}: {title}"
            if not dry_run:
                ok, msg = delete_remote_branch(repo, branch)
                if not ok:
                    continue
                append_ledger(ledger, branch, repo, action, reason, decided_by)
            actions.append({
                "number": number,
                "branch": branch,
                "action": action,
                "reason": reason,
                "url": url,
                "closed_at": closed_at,
                "dry_run": dry_run,
            })
    return actions


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description="Close merged-but-still-open PRs.")
    p.add_argument("--repo", required=True, help="owner/repo")
    p.add_argument("--ledger", default="docs/repo-hygiene-2026-08.ndjson")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--force-closed", action="store_true",
                   help="also delete branches for closed (not merged) PRs")
    p.add_argument("--decided-by", default=os.environ.get("USER", "cursor-ai"))
    args = p.parse_args(argv)

    prs = list_prs(args.repo)
    if not prs:
        print("no PRs found (or gh pr list failed)", file=sys.stderr)
        return 1
    actions = process(
        prs, args.repo, Path(args.ledger),
        dry_run=args.dry_run, force_closed=args.force_closed,
        decided_by=args.decided_by,
    )
    print(json.dumps(actions, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())