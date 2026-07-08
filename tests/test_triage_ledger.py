#!/usr/bin/env python3
"""TDD tests for triage-ledger.py."""
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "tools" / "triage-ledger.py"


def run(args, tmp_path):
    return __import__("subprocess").run(
        [sys.executable, str(SCRIPT), *args],
        cwd=tmp_path,
        capture_output=True, text=True,
    )


class TestTriageLedger:
    def test_appends_valid_entry(self, tmp_path):
        r = run([
            "--branch", "feature/x",
            "--repo", "nfsarch33/helixon-platform",
            "--action", "keep",
            "--reason", "in-progress, will merge in v14519",
            "--decided-by", "cursor-ai",
            "--ledger", "docs/ledger.ndjson",
        ], tmp_path)
        assert r.returncode == 0, r.stderr
        assert (tmp_path / "docs" / "ledger.ndjson").exists()
        lines = (tmp_path / "docs" / "ledger.ndjson").read_text().strip().splitlines()
        assert len(lines) == 1
        entry = json.loads(lines[0])
        assert entry["branch"] == "feature/x"
        assert entry["action"] == "keep"
        assert entry["repo"] == "nfsarch33/helixon-platform"
        assert entry["decided_by"] == "cursor-ai"

    def test_appends_multiple_entries(self, tmp_path):
        for i, action in enumerate(["keep", "close", "merge"]):
            r = run([
                "--branch", f"br-{i}",
                "--repo", "nfsarch33/helixon-platform",
                "--action", action,
                "--ledger", "docs/ledger.ndjson",
            ], tmp_path)
            assert r.returncode == 0, r.stderr
        lines = (tmp_path / "docs" / "ledger.ndjson").read_text().strip().splitlines()
        assert len(lines) == 3
        actions = [json.loads(l)["action"] for l in lines]
        assert actions == ["keep", "close", "merge"]

    def test_rejects_invalid_action(self, tmp_path):
        r = run([
            "--branch", "x",
            "--repo", "nfsarch33/helixon-platform",
            "--action", "delete",  # not in {keep, close, merge, rebase}
            "--ledger", "docs/ledger.ndjson",
        ], tmp_path)
        assert r.returncode != 0


if __name__ == "__main__":
    import pytest
    sys.exit(pytest.main([__file__, "-v"]))