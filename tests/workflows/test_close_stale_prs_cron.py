#!/usr/bin/env python3
"""TDD tests for the weekly-cron workflow file.

These tests validate the workflow YAML without invoking it (we don't
have GitHub Actions in this environment). They cover:
  - workflow file exists
  - cron schedule is set
  - workflow_dispatch is enabled for manual triggers
  - the workflow calls close-stale-prs.py correctly
  - permissions are explicit (no implicit default)
"""
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent
WF = ROOT / ".github" / "workflows" / "close-stale-prs.yml"


class TestWeeklyCronWorkflow:
    def test_workflow_file_exists(self):
        assert WF.exists(), f"missing {WF}"

    def test_workflow_has_cron_schedule(self):
        text = WF.read_text()
        assert "cron:" in text
        # Must include "0 3 * * 0" (Sundays 03:00 UTC)
        assert "0 3 * * 0" in text, f"missing expected cron schedule:\n{text}"

    def test_workflow_has_manual_dispatch(self):
        text = WF.read_text()
        assert "workflow_dispatch:" in text

    def test_workflow_calls_close_stale_prs(self):
        text = WF.read_text()
        assert "tools/close-stale-prs.py" in text, "workflow should call close-stale-prs.py"
        assert "--ledger" in text, "workflow should write to ledger"

    def test_workflow_uses_explicit_permissions(self):
        text = WF.read_text()
        assert "permissions:" in text, "workflow must declare explicit permissions"

    def test_workflow_uploads_artifacts(self):
        text = WF.read_text()
        assert "actions/upload-artifact" in text

    def test_workflow_decided_by_github_actions(self):
        text = WF.read_text()
        assert "--decided-by github-actions" in text or "decided-by github-actions" in text

    def test_workflow_handles_dry_run(self):
        text = WF.read_text()
        assert "--dry-run" in text


if __name__ == "__main__":
    import pytest
    sys.exit(pytest.main([__file__, "-v"]))