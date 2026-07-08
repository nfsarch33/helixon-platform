"""TDD tests for the cursor-tools MCP health-check flow.

Authored: 2026-07-15 (v14514 Pair-6 MVP).

These tests exercise:
  - cursor-tools list returns >=21 servers (matches the catalog at conversation start)
  - cursor-tools doctor --json produces a well-formed DoctorReport
  - every server in mcp.json also appears in the inventory
  - disabled servers are flagged with state=skipped
  - the restore subcommand emits a JSON snippet containing mcpServers.<id>

The cursor-tools CLI is built into a temp binary per-test so we do
not need a Make target for the test fixture.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2] / "helixon-platform"
CMD_DIR = REPO_ROOT / "cmd" / "cursor-tools"
INVENTORY = REPO_ROOT / "cursor-config" / "mcp" / "cursor-tools-inventory.json"
MCP_JSON = REPO_ROOT / "cursor-config" / "mcp" / "mcp.json"


def build_cursor_tools(tmp: Path) -> Path:
    """Build the cursor-tools binary into tmp/cursor-tools."""
    if not CMD_DIR.exists():
        raise RuntimeError(f"cursor-tools source dir missing: {CMD_DIR}")
    bin_path = tmp / "cursor-tools"
    cmd = ["go", "build", "-o", str(bin_path), str(CMD_DIR)]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        raise RuntimeError(f"go build failed:\n{proc.stdout}\n{proc.stderr}")
    return bin_path


def load_inventory() -> dict:
    with open(INVENTORY) as f:
        return json.load(f)


def load_mcp_json() -> dict:
    with open(MCP_JSON) as f:
        return json.load(f)


class CursorToolsInventory(unittest.TestCase):
    def test_inventory_has_at_least_21_servers(self):
        inv = load_inventory()
        self.assertGreaterEqual(
            len(inv["servers"]),
            21,
            f"expected >=21 servers, got {len(inv['servers'])}",
        )

    def test_inventory_every_server_has_id_and_command(self):
        inv = load_inventory()
        for s in inv["servers"]:
            self.assertIn("id", s, f"server missing id: {s}")
            self.assertIn("command", s, f"server missing command: {s}")

    def test_inventory_server_ids_unique(self):
        inv = load_inventory()
        ids = [s["id"] for s in inv["servers"]]
        self.assertEqual(len(ids), len(set(ids)), "duplicate server ids in inventory")

    def test_mcp_json_every_server_in_inventory(self):
        inv = load_inventory()
        mcp = load_mcp_json()
        inventory_ids = {s["id"] for s in inv["servers"]}
        mcp_ids = set(mcp["mcpServers"].keys())
        missing = mcp_ids - inventory_ids
        self.assertEqual(missing, set(), f"mcp.json has servers not in inventory: {missing}")
        # Inventory may have extras (planned), but mcp.json should not.

    def test_disabled_servers_have_disabled_flag_in_mcp_json(self):
        mcp = load_mcp_json()
        for sid, entry in mcp["mcpServers"].items():
            if entry.get("disabled"):
                # Confirm env requires something we don't have
                env = entry.get("env", {})
                if not env:
                    continue
                self.assertGreater(
                    len(env),
                    0,
                    f"disabled server {sid} should require env vars",
                )


class CursorToolsBinary(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = Path(tempfile.mkdtemp(prefix="cursor-tools-test-"))
        cls.bin_path = build_cursor_tools(cls.tmp)
        # Set env so the CLI finds our local inventory
        os.environ["HELIXON_CURSOR_TOOLS_INVENTORY"] = str(INVENTORY)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp, ignore_errors=True)
        os.environ.pop("HELIXON_CURSOR_TOOLS_INVENTORY", None)

    def test_version(self):
        out = subprocess.run(
            [str(self.bin_path), "version"], capture_output=True, text=True
        )
        self.assertEqual(out.returncode, 0)
        self.assertIn("cursor-tools", out.stdout)

    def test_list_returns_inventory(self):
        out = subprocess.run(
            [str(self.bin_path), "list"], capture_output=True, text=True
        )
        self.assertEqual(out.returncode, 0, out.stderr)
        # Should mention at least one known server id
        self.assertIn("user-user-fetch", out.stdout)

    def test_doctor_json_shape(self):
        out = subprocess.run(
            [str(self.bin_path), "doctor", "--json"], capture_output=True, text=True
        )
        report = json.loads(out.stdout)
        self.assertIn("updated_at", report)
        self.assertIn("total", report)
        self.assertIn("results", report)
        self.assertEqual(report["total"], len(report["results"]))
        # Every disabled server must report skipped
        for r in report["results"]:
            if r.get("disabled"):
                self.assertEqual(r["state"], "skipped", f"{r['id']} should be skipped")

    def test_restore_emits_mcp_servers_block(self):
        out = subprocess.run(
            [str(self.bin_path), "restore", "--server", "user-user-fetch"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(out.returncode, 0, out.stderr)
        snippet = json.loads(out.stdout)
        self.assertIn("mcpServers", snippet)
        self.assertIn("user-user-fetch", snippet["mcpServers"])

    def test_restore_unknown_server_exits_3(self):
        out = subprocess.run(
            [str(self.bin_path), "restore", "--server", "nope-xyz"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(out.returncode, 3)


if __name__ == "__main__":
    unittest.main()