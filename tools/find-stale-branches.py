#!/usr/bin/env python3
"""find-stale-branches — list git branches that look stale.

Stale is defined as any of:
  - last commit >= stale_days ago (default 30)
  - no associated open PR
  - branch name matches a stale pattern (wip-, scratch/, test-)

Usage:
    find-stale-branches.py [--stale-days N] [--repo PATH] [--format json|text]
                            [--include-merged]

Exit codes:
    0  always (informational tool)
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, asdict
from datetime import datetime, timezone
from pathlib import Path

STALE_PATTERN_RE = re.compile(r"(wip-|scratch/|test-|tmp-|do-not-merge)", re.IGNORECASE)


@dataclass
class BranchInfo:
    name: str
    last_commit_at: str | None
    last_commit_age_days: int | None
    is_merged: bool
    has_open_pr: bool
    is_stale_pattern: bool
    stale_reasons: list[str]


def _git(*args: str, cwd: Path) -> str:
    r = subprocess.run(
        ["git", *args],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
    )
    if r.returncode != 0:
        return ""
    return r.stdout.strip()


def _parse_iso(s: str) -> datetime | None:
    try:
        # git for-each-date emits e.g. "2026-07-15T01:23:45+10:00"
        return datetime.fromisoformat(s.replace("Z", "+00:00")).astimezone(timezone.utc)
    except (ValueError, AttributeError):
        return None


def list_branches(repo: Path, include_merged: bool = False) -> list[str]:
    out = _git(
        "for-each-ref",
        "--format=%(refname:short)",
        "refs/heads/",
        cwd=repo,
    )
    branches = [b for b in out.splitlines() if b]
    if include_merged:
        return branches
    # default: drop already-merged branches (merged into HEAD), but
    # NOT the current branch (HEAD shows itself in --merged).
    head_out = _git("rev-parse", "--abbrev-ref", "HEAD", cwd=repo)
    head = head_out.strip()
    merged_out = _git("branch", "--merged", "HEAD", "--format=%(refname:short)", cwd=repo)
    merged = set(merged_out.splitlines())
    merged.discard(head)
    return [b for b in branches if b not in merged]


def branch_commit_date(repo: Path, branch: str) -> tuple[str | None, datetime | None]:
    out = _git("log", "-1", "--format=%cI", branch, cwd=repo)
    return (out or None), _parse_iso(out or "")


def is_merged(repo: Path, branch: str) -> bool:
    out = _git("branch", "--merged", "HEAD", "--format=%(refname:short)", cwd=repo)
    return branch in set(out.splitlines())


def has_open_pr(repo: Path, branch: str) -> bool:
    """Best-effort: ask `gh pr list --head <branch>`."""
    if not _which("gh"):
        return False
    r = subprocess.run(
        ["gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number"],
        cwd=repo, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0:
        return False
    try:
        items = json.loads(r.stdout)
        return len(items) > 0
    except json.JSONDecodeError:
        return False


def _which(cmd: str) -> str | None:
    from shutil import which
    return which(cmd)


def classify(
    repo: Path,
    branches: list[str],
    stale_days: int,
    now: datetime | None = None,
    include_merged: bool = False,
) -> list[BranchInfo]:
    now = now or datetime.now(timezone.utc)
    results: list[BranchInfo] = []
    for b in branches:
        date_str, date = branch_commit_date(repo, b)
        age = (now - date).days if date else None
        merged = is_merged(repo, b)
        pr = has_open_pr(repo, b)
        pat = bool(STALE_PATTERN_RE.search(b))
        reasons: list[str] = []
        if age is not None and age >= stale_days:
            reasons.append(f"last_commit_age_days={age}>={stale_days}")
        if not pr:
            reasons.append("no_open_pr")
        if pat:
            reasons.append(f"name_matches_stale_pattern")
        if not include_merged and merged:
            reasons.append("merged_into_HEAD")
        results.append(
            BranchInfo(
                name=b,
                last_commit_at=date_str,
                last_commit_age_days=age,
                is_merged=merged,
                has_open_pr=pr,
                is_stale_pattern=pat,
                stale_reasons=reasons,
            )
        )
    return results


def format_text(rows: list[BranchInfo]) -> str:
    out = [f"{'BRANCH':<40} {'AGE':>5} {'MERGED':>7} {'PR':>4}  REASONS"]
    out.append("-" * 80)
    for r in rows:
        age = str(r.last_commit_age_days) if r.last_commit_age_days is not None else "-"
        merged = "yes" if r.is_merged else "no"
        pr = "yes" if r.has_open_pr else "no"
        reasons = "; ".join(r.stale_reasons) if r.stale_reasons else "(clean)"
        out.append(f"{r.name:<40} {age:>5} {merged:>7} {pr:>4}  {reasons}")
    return "\n".join(out)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description="List stale git branches.")
    p.add_argument("--repo", default=".", help="git repo path (default cwd)")
    p.add_argument("--stale-days", type=int, default=30, help="days threshold (default 30)")
    p.add_argument(
        "--format",
        choices=["text", "json"],
        default="text",
        help="output format (default text)",
    )
    p.add_argument(
        "--include-merged",
        action="store_true",
        help="include branches already merged into HEAD",
    )
    p.add_argument(
        "--now",
        default=None,
        help="override now (ISO 8601), useful for tests",
    )
    args = p.parse_args(argv)
    repo = Path(args.repo).resolve()
    if not (repo / ".git").exists():
        print(f"error: {repo} is not a git repo", file=sys.stderr)
        return 2
    now = _parse_iso(args.now) if args.now else None
    branches = list_branches(repo, include_merged=args.include_merged)
    rows = classify(repo, branches, args.stale_days, now=now, include_merged=args.include_merged)
    if args.format == "json":
        print(json.dumps([asdict(r) for r in rows], indent=2))
    else:
        print(format_text(rows))
    return 0


if __name__ == "__main__":
    sys.exit(main())