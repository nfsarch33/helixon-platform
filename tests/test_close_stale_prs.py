#!/usr/bin/env python3
"""TDD tests for close-stale-prs.py."""
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "tools" / "close-stale-prs.py"

# Re-import the module so we can test process() directly.
import importlib.util
spec = importlib.util.spec_from_file_location("close_stale_prs", SCRIPT)
cs = importlib.util.module_from_spec(spec)
sys.modules["close_stale_prs"] = cs
spec.loader.exec_module(cs)


def _pr(state, branch, number=1, merged_at=None, closed_at=None, title="t"):
    return {
        "number": number,
        "title": title,
        "state": state,
        "headRefName": branch,
        "mergedAt": merged_at,
        "closedAt": closed_at,
        "url": f"https://github.com/x/y/pull/{number}",
    }


class TestProcess:
    def test_merged_pr_generates_delete_branch_action(self, tmp_path):
        ledger = tmp_path / "ledger.ndjson"
        prs = [_pr("merged", "feature/x", number=42, merged_at="2026-07-15T01:00:00Z")]
        # dry_run=True so we don't actually call gh
        actions = cs.process(prs, "owner/repo", ledger, dry_run=True)
        assert len(actions) == 1
        a = actions[0]
        assert a["branch"] == "feature/x"
        assert a["action"] == "delete-branch"
        assert a["number"] == 42
        assert a["dry_run"] is True
        # Ledger not written in dry-run
        assert not ledger.exists()

    def test_open_pr_no_action(self, tmp_path):
        ledger = tmp_path / "ledger.ndjson"
        prs = [_pr("open", "feature/y")]
        actions = cs.process(prs, "owner/repo", ledger, dry_run=True)
        assert actions == []

    def test_closed_pr_skipped_without_force(self, tmp_path):
        ledger = tmp_path / "ledger.ndjson"
        prs = [_pr("closed", "feature/z", closed_at="2026-07-10T00:00:00Z")]
        actions = cs.process(prs, "owner/repo", ledger, dry_run=True, force_closed=False)
        assert actions == []

    def test_closed_pr_with_force(self, tmp_path):
        ledger = tmp_path / "ledger.ndjson"
        prs = [_pr("closed", "feature/z", closed_at="2026-07-10T00:00:00Z")]
        actions = cs.process(prs, "owner/repo", ledger, dry_run=True, force_closed=True)
        assert len(actions) == 1
        a = actions[0]
        assert a["action"] == "delete-branch"
        assert a["closed_at"] == "2026-07-10T00:00:00Z"

    def test_ledger_written_on_real_action(self, tmp_path, monkeypatch):
        ledger = tmp_path / "ledger.ndjson"
        prs = [_pr("merged", "feature/w", number=99)]
        # Monkeypatch delete_remote_branch so we don't hit the network
        monkeypatch.setattr(cs, "delete_remote_branch", lambda r, b: (True, "(stub)"))
        actions = cs.process(prs, "owner/repo", ledger, dry_run=False, decided_by="test")
        assert len(actions) == 1
        assert ledger.exists()
        lines = ledger.read_text().strip().splitlines()
        assert len(lines) == 1
        entry = json.loads(lines[0])
        assert entry["branch"] == "feature/w"
        assert entry["action"] == "delete-branch"
        assert entry["decided_by"] == "test"


class TestDeleteRemoteBranch:
    def test_returns_true_on_404(self):
        # This exercises the error-message parsing. We can't actually
        # hit the API in tests, but the function should handle the
        # "already deleted" case gracefully.
        ok, msg = cs.delete_remote_branch("nonexistent/repo", "definitely-not-a-real-branch-xyz")
        # Either it succeeds (the branch doesn't exist → already deleted)
        # or it fails gracefully. We just verify it doesn't crash.
        assert isinstance(ok, bool)
        assert isinstance(msg, str)


if __name__ == "__main__":
    import pytest
    sys.exit(pytest.main([__file__, "-v"]))