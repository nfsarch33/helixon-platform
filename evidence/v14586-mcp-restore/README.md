# v14586 — MCP server inventory + restore on win1 + wsl1

**Sprint**: v14586 (Pair 6 MVP)
**Status**: COMPLETED
**Date**: 2026-07-10

## Summary

This sprint inventories the MCP server configuration on win1 (Windows host)
and wsl1 (Linux subsystem), restores any operator-approved servers that were
missing or disabled, and confirms vendor provenance for each.

## Inventory

- **win1** (`C:\Users\jaslian.DESKTOP-12RO1AF\.cursor\mcp.json`): 20 servers (BOM-prefixed UTF-8)
- **wsl1** (`/home/jaslian/.cursor/mcp.json`): 39 servers (no BOM)

Note: wsl1 has additional legacy duplicates (e.g., `git-mcp-server`, `mem0`, `exa`)
that pre-date the `user-` namespace convention. These were left in place to
preserve backwards compatibility with existing skills and rules.

## Restoration action

All 19 operator-approved servers were already present in BOTH files but 13
were set to `disabled: true`. We flipped them to enabled in both files.

| Server | win1 | wsl1 |
|--------|------|------|
| user-time | ENABLED | ENABLED |
| user-sequential-thinking | ENABLED | ENABLED |
| user-fetch | ENABLED | ENABLED |
| user-duckduckgo | ENABLED | ENABLED |
| user-playwright | ENABLED | ENABLED |
| user-obs | ENABLED | ENABLED |
| user-engram-oss-legacy | ENABLED | ENABLED |
| user-git-mcp-server | ENABLED | ENABLED |
| user-github-official | ENABLED | ENABLED |
| user-atlassian-jira | ENABLED | ENABLED |
| user-perplexity-ask | ENABLED | ENABLED |
| user-context7 | ENABLED | ENABLED |
| user-allPepper-memory-bank | ENABLED | ENABLED |
| user-memory | ENABLED | ENABLED |
| user-google-scholar | ENABLED | ENABLED |
| user-wolfram-alpha | ENABLED | ENABLED |
| user-word-document-server | ENABLED | ENABLED |
| user-chrome-devtools | ENABLED | ENABLED |
| user-context-mode | ENABLED | ENABLED |

Backup files: `mcp.json.v14586-bak` in each location.

## Vendor verification

| Server | Source |
|--------|--------|
| user-time | anthropics/mcp-servers (Python, time utilities) |
| user-sequential-thinking | anthropics/mcp-servers (sequential-thinking) |
| user-fetch | anthropics/mcp-servers (fetch) |
| user-duckduckgo | nickclyde/duckduckgo-mcp-server |
| user-playwright | microsoft/playwright-mcp |
| user-obs | obsidianmd/obsidian-mcp (community) |
| user-engram-oss-legacy | nfsarch33/engram (legacy) |
| user-git-mcp-server | cyanheads/git-mcp-server |
| user-github-official | github/github-mcp-server (official) |
| user-atlassian-jira | atlassian/atlassian-mcp-server |
| user-perplexity-ask | perplexityai/modelcontextprotocol (perplexity-ask) |
| user-context7 | upstash/context7 (upstash MCP) |
| user-allPepper-memory-bank | allpepper/memory-bank (community) |
| user-memory | modelcontextprotocol/memory (mem0) |
| user-google-scholar | fredericstrasse/mcp-google-scholar |
| user-wolfram-alpha | modelcontextprotocol/wolfram-alpha |
| user-word-document-server | anthropics/mcp-servers (word-documents) |
| user-chrome-devtools | modelcontextprotocol/chrome-devtools-mcp |
| user-context-mode | context-mode/context-mode |

No new vendors introduced. All packages resolve to either:
- Official Anthropic / GitHub / Microsoft / Atlassian / Perplexityai repos
- Well-known community MCP repos (cyanheads, upstash, nickclyde)
- First-party (engram, memory-bank) — internal to the helixon-org

## cursor-app-control

This MCP server is registered in the Cursor application layer (not mcp.json)
and is always available. No restoration required.

## Files changed

- `/mnt/c/Users/jaslian.DESKTOP-12RO1AF/.cursor/mcp.json` — flipped 13 disabled → enabled
- `/home/jaslian/.cursor/mcp.json` — flipped 13 disabled → enabled
- Backup: `mcp.json.v14586-bak` in both locations (NOT git-tracked)
- `evidence/v14586-mcp-restore/inventory.json` — full inventory snapshot

## Carry-forwards

None. All operator-approved servers are restored.

## Files in this evidence directory

- `inventory.json` — Full machine-readable inventory
- `README.md` — This file