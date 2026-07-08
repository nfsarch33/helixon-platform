#!/usr/bin/env python3
"""triage-ledger — append entries to a triage ledger (NDJSON).

Each entry:
  {ts, branch, repo, action, reason, decided_by}

Usage:
    triage-ledger.py --branch BR --repo OWNER/REPO --action {keep,close,merge,rebase}
                     [--reason TEXT] [--decided-by ID] [--ledger PATH]

The default ledger is docs/repo-hygiene-2026-08.ndjson.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

VALID_ACTIONS = {"keep", "close", "merge", "rebase"}


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description="Append triage entries.")
    p.add_argument("--branch", required=True)
    p.add_argument("--repo", required=True, help="owner/repo")
    p.add_argument("--action", required=True, choices=sorted(VALID_ACTIONS))
    p.add_argument("--reason", default="")
    p.add_argument("--decided-by", default=os.environ.get("USER", "unknown"))
    p.add_argument("--ledger", default="docs/repo-hygiene-2026-08.ndjson")
    args = p.parse_args(argv)

    entry = {
        "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "branch": args.branch,
        "repo": args.repo,
        "action": args.action,
        "reason": args.reason,
        "decided_by": args.decided_by,
    }
    ledger = Path(args.ledger)
    ledger.parent.mkdir(parents=True, exist_ok=True)
    with ledger.open("a") as f:
        f.write(json.dumps(entry) + "\n")
    print(f"appended: {args.action} {args.branch} ({args.repo})")
    return 0


if __name__ == "__main__":
    sys.exit(main())