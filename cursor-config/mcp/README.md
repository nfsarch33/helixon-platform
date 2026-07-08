# Helixon MCP inventory (`cursor-config/mcp/`)

Authored 2026-07-15 as part of **v14514 Pair 6 MVP**.

## Files

- `cursor-tools-inventory.json` — canonical list of all 21 MCP servers
  the Helixon fleet expects, with `disabled`/`enabled` flags and
  `notes` documenting required env vars (e.g. `GITHUB_PERSONAL_ACCESS_TOKEN`,
  `JIRA_URL`, etc.).
- `mcp.json` — the actual `mcpServers` block Cursor loads. Mirrors the
  inventory but is the wire-format file.
- `cmd/cursor-tools/` — Go CLI that owns this config. Subcommands:
  - `list` — print the inventory summary.
  - `doctor` — ping each server (binary existence + disabled state).
    Emits JSON or human output. Exits non-zero on any FAIL.
  - `restore --server <id>` — emit a single-server JSON snippet, ready
    to paste into `~/.cursor/mcp.json`.
  - `config` — print the merged `mcp.json`.

## Enabling a disabled server

Most servers are disabled because they require an env var we do not yet
have. To enable:

1. Add the credential to 1Password under `Cursor_IronClaw`.
2. Pull it via `op read 'op://Cursor_IronClaw/<item>/<field>'`.
3. Export the env var in `~/.cursor/.envrc` (or system-wide).
4. Edit `mcp.json` and remove the `"disabled": true` line for that server.
5. Run `cursor-tools doctor --json` and confirm `state=ok`.

## Operator bring-up checklist

```bash
# Build the CLI
go build -o /usr/local/bin/cursor-tools ./cmd/cursor-tools/

# Run the doctor
HELIXON_CURSOR_TOOLS_INVENTORY=$PWD/cursor-config/mcp/cursor-tools-inventory.json \
  cursor-tools doctor --json | jq '{total, ok, failed, skipped}'

# Install the config into Cursor's settings dir
mkdir -p ~/.cursor
cp cursor-config/mcp/mcp.json ~/.cursor/mcp.json
```

## Acceptance for v14514

- `cursor-tools list` returns 21 servers.
- `cursor-tools doctor --json` produces a `DoctorReport` JSON document.
- `cursor-tools restore --server <id>` emits a valid `mcpServers.<id>` block.
- `tests/test_mcp_health.py` passes 10/10.
- `cmd/cursor-tools/main_test.go` passes (race-clean).
- Tier-4 cross-layer verifier still >= 53 PASS.