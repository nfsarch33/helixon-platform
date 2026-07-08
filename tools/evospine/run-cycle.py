#!/usr/bin/env python3
"""evospine cycle driver (v14520).

A single EvoSpine cycle:

  obs       gather observations (e.g., weekly git/PR metrics)
  hypothesize  propose a patch (file edits, command sequence)
  patch       apply the patch in a worktree
  eval        run the eval (pytest, gh queries, etc.)
  commit      record the result in the sprint ledger

This CLI drives the cycle end-to-end. Every stage emits a structured
record to stdout; the operator (or a future MCP) can inspect them.

Stages run in order; if any fails, the cycle aborts and writes a
failure record.

Usage:
    evospine-cycle.py --repo OWNER/REPO --branch NAME
                      [--obs-cmd CMD] [--patch-cmd CMD] [--eval-cmd CMD]
                      [--dry-run]

The default commands:
  obs:    python3 tools/find-stale-branches.py --repo <repo> --stale-days 30 --include-merged --format json
  patch:  echo "(no-op patch)"
  eval:   python3 -m pytest tests/ -q
  commit: git add -A && git commit -m "evospine: <hypothesis>"

Exit codes:
  0   cycle succeeded
  1   cycle failed at any stage
  2   invalid arguments
"""
from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
import time
import uuid
from dataclasses import dataclass, field, asdict
from datetime import datetime, timezone
from pathlib import Path


@dataclass
class CycleRecord:
    cycle_id: str
    repo: str
    branch: str
    started_at: str
    obs: dict = field(default_factory=dict)
    hypothesis: dict = field(default_factory=dict)
    patch: dict = field(default_factory=dict)
    eval: dict = field(default_factory=dict)
    commit: dict = field(default_factory=dict)
    finished_at: str = ""
    status: str = "running"


def _run(cmd: str, cwd: Path) -> tuple[int, str, str]:
    # Use shell=True so commands like `git commit -m "..."` work.
    # Inputs are constructed internally (no user-supplied shell).
    r = subprocess.run(
        cmd,
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
        shell=True,
    )
    return r.returncode, r.stdout, r.stderr


def stage_obs(repo: str, cwd: Path, dry_run: bool) -> dict:
    if dry_run:
        return {"stage": "obs", "status": "skipped", "reason": "dry-run"}
    # If repo looks like a local path, use it directly; otherwise
    # treat it as owner/name and use cwd as the local path.
    repo_path = Path(repo)
    if not (repo_path / ".git").exists():
        # Treat as owner/name; use cwd as the local path
        local = cwd
    else:
        local = repo_path
    cmd = f"python3 tools/find-stale-branches.py --repo {local} --stale-days 30 --include-merged --format json"
    rc, out, err = _run(cmd, cwd)
    try:
        data = json.loads(out) if out else []
    except json.JSONDecodeError:
        data = [{"raw_stdout": out[:500], "stderr": err[:500]}]
    return {
        "stage": "obs",
        "status": "ok" if rc == 0 else "failed",
        "returncode": rc,
        "items": len(data) if isinstance(data, list) else 0,
        "data": data if isinstance(data, list) and len(data) <= 5 else (data[:5] if isinstance(data, list) else data),
    }


def stage_hypothesize(obs: dict) -> dict:
    """Propose a patch based on observations.

    Heuristic v14520:
      - If obs shows >= 3 merged-but-stale branches → hypothesis is
        "run close-stale-prs weekly cron".
      - If obs shows no stale branches → "no action needed".
    """
    items = obs.get("items", 0)
    if items >= 3:
        hyp = "Weekly cron + GitHub App > manual batch API (deferred to v14520 carry-forward)"
        return {"stage": "hypothesize", "status": "ok", "hypothesis": hyp, "evidence": f"obs.items={items}>=3"}
    elif items == 0:
        return {"stage": "hypothesize", "status": "ok", "hypothesis": "no action needed (clean repo)", "evidence": "obs.items=0"}
    else:
        return {"stage": "hypothesize", "status": "ok", "hypothesis": "monitor but do not auto-act", "evidence": f"obs.items={items}<3"}


def stage_patch(repo: str, branch: str, cwd: Path, hypothesis: dict, dry_run: bool) -> dict:
    if dry_run:
        return {"stage": "patch", "status": "skipped", "reason": "dry-run", "hypothesis": hypothesis.get("hypothesis")}
    # In v14520 the patch is a no-op (we already deleted the branches
    # in v14519). Future cycles will write files here.
    return {
        "stage": "patch",
        "status": "ok",
        "applied": False,
        "reason": "v14520 cycle is observational; v14521 will apply a weekly cron .github/workflows/close-stale-prs.yml",
        "hypothesis": hypothesis.get("hypothesis"),
    }


def stage_eval(cwd: Path, dry_run: bool) -> dict:
    if dry_run:
        return {"stage": "eval", "status": "skipped", "reason": "dry-run"}
    # Run a fast subset of pytest (the close-stale-prs + find-stale tests).
    cmd = "python3 -m pytest tests/test_close_stale_prs.py tests/test_find_stale_branches.py tests/test_triage_ledger.py -q"
    rc, out, err = _run(cmd, cwd)
    summary = out.strip().splitlines()[-1] if out.strip() else ""
    return {
        "stage": "eval",
        "status": "ok" if rc == 0 else "failed",
        "returncode": rc,
        "summary": summary,
        "stderr_tail": err[-300:] if err else "",
    }


def stage_commit(repo: str, branch: str, cwd: Path, record: dict, dry_run: bool) -> dict:
    if dry_run:
        return {"stage": "commit", "status": "skipped", "reason": "dry-run"}
    # Write the cycle record to evospine-cycles.ndjson and commit
    ledger = cwd / "evospine-cycles.ndjson"
    ledger.parent.mkdir(parents=True, exist_ok=True)
    line = json.dumps(record, default=str)
    with ledger.open("a") as f:
        f.write(line + "\n")
    # Stage + commit
    add_rc, _, _ = _run("git add -A", cwd)
    if add_rc != 0:
        return {"stage": "commit", "status": "failed", "reason": "git add failed"}
    msg = f"evospine: cycle {record['cycle_id']} ({record['hypothesis'].get('hypothesis', 'unknown')[:60]})"
    commit_rc, commit_out, commit_err = _run(f'git commit -m "{msg}"', cwd)
    return {
        "stage": "commit",
        "status": "ok" if commit_rc == 0 else "failed",
        "returncode": commit_rc,
        "message": msg,
        "stderr_tail": commit_err[-200:] if commit_err else "",
    }


def run_cycle(
    repo: str,
    branch: str,
    cwd: Path,
    dry_run: bool = False,
) -> CycleRecord:
    cycle_id = f"evospine-{datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S')}-{uuid.uuid4().hex[:6]}"
    rec = CycleRecord(
        cycle_id=cycle_id,
        repo=repo,
        branch=branch,
        started_at=datetime.now(timezone.utc).isoformat(),
    )
    # obs
    rec.obs = stage_obs(repo, cwd, dry_run)
    if rec.obs.get("status") == "failed":
        rec.status = "failed_at_obs"
        rec.finished_at = datetime.now(timezone.utc).isoformat()
        return rec
    # hypothesize
    rec.hypothesis = stage_hypothesize(rec.obs)
    # patch
    rec.patch = stage_patch(repo, branch, cwd, rec.hypothesis, dry_run)
    # eval
    rec.eval = stage_eval(cwd, dry_run)
    if rec.eval.get("status") == "failed":
        rec.status = "failed_at_eval"
        rec.finished_at = datetime.now(timezone.utc).isoformat()
        return rec
    # commit
    rec.commit = stage_commit(repo, branch, cwd, asdict(rec), dry_run)
    if rec.commit.get("status") == "failed":
        rec.status = "failed_at_commit"
    else:
        rec.status = "succeeded"
    rec.finished_at = datetime.now(timezone.utc).isoformat()
    return rec


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description="Run one EvoSpine cycle.")
    p.add_argument("--repo", required=True, help="owner/repo")
    p.add_argument("--branch", default="main", help="branch to operate on")
    p.add_argument("--cwd", default=".", help="working directory")
    p.add_argument("--dry-run", action="store_true")
    args = p.parse_args(argv)
    cwd = Path(args.cwd).resolve()
    rec = run_cycle(args.repo, args.branch, cwd, dry_run=args.dry_run)
    print(json.dumps(asdict(rec), indent=2))
    return 0 if rec.status == "succeeded" else 1


if __name__ == "__main__":
    sys.exit(main())