# runx-public-repo-gate: allow-file fleet_host_alias
"""
TDD test: op cli write path on wsl1 must work.

Background
----------
The op 2.34.1 CLI on wsl1 hangs indefinitely when `op item create` is
invoked. Root cause: op CLI 2.x defers all write operations through the
1Password desktop app integration (settings.json at
~/.config/1Password/settings/settings.json). When the desktop app is not
installed on the Windows host, op waits forever for the desktop IPC
channel and never returns.

This test verifies that the v14508.5 fallback path (1Password Go SDK
bootstrap CLI) is available so the closeout plan can proceed without
the desktop app.

Acceptance criteria
-------------------
1. `op whoami` must return a service-account identity within 5 s.
2. `op vault list` must return the Cursor_IronClaw vault.
3. `op read` of the existing `Win PC WSL Ubuntu Login GB` password must
   return a non-empty value.
4. The `onepassword-bootstrap` binary must build with `go build`.
5. Re-running `onepassword-bootstrap` must be idempotent (skip-existing).

Test layout
-----------
- Requires the Go toolchain (>= 1.24) and op CLI 2.x on PATH.
- Reads the service account token from ~/.config/op/service-account-token.
- Skips gracefully if the token file is absent (this is an environment
  check, not a hard failure).
"""

import os
import shutil
import subprocess
import time
import unittest
from pathlib import Path

OP = shutil.which("op")
WSL1_HOME = Path("/home/jaslian")
SERVICE_ACCOUNT_TOKEN = WSL1_HOME / ".config/op/service-account-token"
EXISTING_VAULT = "Cursor_IronClaw"
EXISTING_ITEM = "Win PC WSL Ubuntu Login GB"
EXISTING_FIELD = "password"
TIMEOUT_S = 5


def run(cmd, timeout=TIMEOUT_S, env=None):
    """Run a shell command with a hard timeout; return (rc, stdout, stderr)."""
    if isinstance(cmd, str):
        cmd = ["/bin/bash", "-lc", cmd]
    try:
        r = subprocess.run(
            cmd, capture_output=True, text=True, timeout=timeout, env=env,
        )
        return r.returncode, r.stdout, r.stderr
    except subprocess.TimeoutExpired as e:
        return 124, e.stdout or "", (e.stderr or "") + f"\nTIMEOUT after {timeout}s"


@unittest.skipIf(OP is None, "op CLI not on PATH")
@unittest.skipIf(
    not SERVICE_ACCOUNT_TOKEN.exists(),
    f"service account token not found at {SERVICE_ACCOUNT_TOKEN}; "
    "this test is environment-gated, not a hard failure",
)
class OpCliWritePathTest(unittest.TestCase):
    """Verifies the op CLI + 1Password Go SDK bootstrap is functional on wsl1."""

    @classmethod
    def setUpClass(cls):
        # Export the service account token into the env so op picks it up.
        cls.token = SERVICE_ACCOUNT_TOKEN.read_text().strip()
        cls.env = os.environ.copy()
        cls.env["OP_SERVICE_ACCOUNT_TOKEN"] = cls.token

    def test_01_whoami_returns_service_account(self):
        """op whoami must complete within 5 s and report SERVICE_ACCOUNT."""
        rc, out, err = self._op("op whoami")
        self.assertEqual(rc, 0, f"op whoami failed (rc={rc}):\nstdout={out}\nstderr={err}")
        self.assertIn("SERVICE_ACCOUNT", out, f"op whoami did not report SERVICE_ACCOUNT: {out}")

    def test_02_vault_list_contains_cursor_ironclaw(self):
        """op vault list must include the Cursor_IronClaw vault."""
        rc, out, err = self._op("op vault list")
        self.assertEqual(rc, 0, f"op vault list failed (rc={rc}):\nstdout={out}\nstderr={err}")
        self.assertIn(EXISTING_VAULT, out, f"vault {EXISTING_VAULT} not in op vault list:\n{out}")

    def test_03_read_existing_login_password(self):
        """op read of the existing universal-password Login item must succeed."""
        ref = f"op://{EXISTING_VAULT}/{EXISTING_ITEM}/{EXISTING_FIELD}"
        rc, out, err = self._op(f"op read '{ref}'")
        self.assertEqual(rc, 0, f"op read failed (rc={rc}):\nstdout={out}\nstderr={err}")
        self.assertGreater(len(out.strip()), 0, "op read returned empty password")
        self.assertGreaterEqual(len(out.strip()), 8, f"password too short: {out!r}")

    def test_04_item_create_hangs_without_desktop_app(self):
        """op item create on wsl1 without the 1Password desktop app hangs.

        This is a regression guard. The v14508.5 plan replaces this with the
        Go SDK bootstrap; we assert the hang here so future Cursor sessions
        can see immediately that the desktop app is still missing.
        """
        rc, _, _ = self._op(
            f"op item create --category login --title 'v14508.5-regression' "
            f"--vault {EXISTING_VAULT} username=test password=test",
            timeout=3,  # hard 3 s; op will not return
        )
        self.assertEqual(
            rc, 124,
            "op item create should still hang on wsl1 until the 1Password "
            "desktop app is installed; if this test passes with rc=0, the "
            "blocker has been resolved and the SDK bootstrap can be retired.",
        )

    def test_05_bootstrap_binary_builds(self):
        """The 1Password Go SDK bootstrap must build with the repo's go.mod."""
        repo = Path("/home/jaslian/Code/helixon-platform")
        if not (repo / "go.mod").exists():
            self.skipTest(f"repo not found at {repo}")
        rc, out, err = self._op(
            f"cd {repo} && go build -o /tmp/onepassword-bootstrap "
            f"./cmd/onepassword-bootstrap",
            timeout=60,
        )
        self.assertEqual(
            rc, 0,
            f"go build failed (rc={rc}):\nstdout={out}\nstderr={err}",
        )
        self.assertTrue(
            Path("/tmp/onepassword-bootstrap").exists(),
            "bootstrap binary not produced",
        )

    def test_06_bootstrap_idempotent(self):
        """Re-running the bootstrap must skip the existing items, not duplicate."""
        repo = Path("/home/jaslian/Code/helixon-platform")
        if not (repo / "go.mod").exists():
            self.skipTest(f"repo not found at {repo}")
        # Use a dummy password (the bootstrap will skip jason@win2/win4 which
        # already exist; HF_TOKEN is not in env so it is not touched).
        bootstrap_env = self.env.copy()
        bootstrap_env["HELIXON_UNIVERSAL_PASSWORD"] = "dummy-test-password-1234567890"
        try:
            r = subprocess.run(
                ["/tmp/onepassword-bootstrap", "--vault", EXISTING_VAULT, "--timeout", "30s"],
                capture_output=True, text=True, timeout=60, env=bootstrap_env,
            )
        except subprocess.TimeoutExpired as e:
            self.fail(f"bootstrap timed out: stdout={e.stdout}\nstderr={e.stderr}")
        self.assertEqual(
            r.returncode, 0,
            f"bootstrap failed (rc={r.returncode}):\nstdout={r.stdout}\nstderr={r.stderr}",
        )
        # The jason@win2/win4 items already exist; bootstrap must report skip.
        # The bootstrap logs go to stderr (log package default), not stdout.
        combined = (r.stdout or "") + (r.stderr or "")
        self.assertIn("skip:", combined, f"expected skip line, got stdout={r.stdout!r} stderr={r.stderr!r}")
        self.assertIn("jason@win2", combined)
        self.assertIn("jason@win4", combined)

    def _op(self, cmd, timeout=TIMEOUT_S):
        """Run an op/go command with the service account token exported."""
        return run(cmd, timeout=timeout, env=self.env)


if __name__ == "__main__":
    unittest.main(verbosity=2)
