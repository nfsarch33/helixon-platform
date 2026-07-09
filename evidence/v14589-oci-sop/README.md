# v14589 — OCI jump SOP update + browser automation playbook

**Date**: 2026-07-10 (UTC+10)
**Sprint**: v14589 (Pair 7 Review)
**Operator directive**: Use `agent-browser` (Playwright/Chromium) for OCI web UI tasks, not CLI scripting.

---

## TL;DR

- ✅ Created new SOP: `sop/oracle-cloud-web-ui-automation.md` — 3 self-service Playbook A/B/C for SSH key refresh, instance Start, subnet route approval.
- ✅ Updated existing SOP: `sop/oracle-cloud-tailscale-jump-server.md` — added v14588 CF-v14588-01 reference, removed MacBook-era language in preflight checklist, added browser-automation row to operations table.
- ✅ Updated hook spec: `sop/before-shell-execution-hook-spec.md` — added Check 11 ("browser-automation preferred when oci-cli unavailable") with synthetic test stub.
- ✅ Resolved CF-v14588-03: deleted redundant `~/.ssh/oracle_jump` keypair; documented decision in `cf-v14588-03-resolution.md`.
- ⏭️ Deferred to operator: CF-v14588-01 (actual SSH key push + oci-sydney-jump restart) — requires OCI console MFA.

---

## Deliverables

### 1. New file: `sop/oracle-cloud-web-ui-automation.md` (v14589)

Three self-service Playbooks:

| Playbook | Purpose | When to use |
|----------|---------|-------------|
| **A — Refresh SSH key** | Push `~/.ssh/fleet_lan.pub` to `oracle-jump`'s `ubuntu` user | CF-v14588-01; key rotation |
| **B — Restart OFFLINE instance** | Start `oci-sydney-jump` from web console | CF-v14588-01; Oracle reclaim |
| **C — Approve Tailscale routes** | Approve fleet node subnet routes in Tailscale admin | New fleet node onboarding |

Each Playbook documents:
- Prerequisites (browser, MFA, key path)
- Step-by-step `agent-browser` commands
- Idempotency guards (`grep -qxF "$PUBKEY" || echo ...`)
- Fallback to manual OCI Cloud Shell if `agent-browser` is broken
- Evidence capture (screenshots to `evidence/<YYYYMMDD>/`)

### 2. Updated file: `sop/oracle-cloud-tailscale-jump-server.md` (v14589)

Changes:

| Change | Reason |
|--------|--------|
| Header metadata updated: "updated v14588 + v14589" | Audit trail |
| "Why a jump server" block replaced with non-MacBook rationale | MacBook is RETIRED |
| Cross-reference to `oracle-cloud-web-ui-automation.md` added | Link Playbooks A/B/C |
| New "Tailscale auth keys currently in 1Password" sub-table | Tracks expiry (CF-v14588-02) |
| Pre-flight Checklist rows updated to current operator info | MacBook-era instructions removed |
| New CF-v14588-01 callout | Operator awareness |
| §7 heading renamed "SSH Configuration (no longer MacBook-specific as of v14589)" | MacBook is RETIRED |
| New operations row: "Push operator SSH key via browser automation (CF-v14588-01)" | Operators know how to recover |
| Maintainer footer updated to `jaslian@desktop-12ro1af-wsl1` | New operator |

### 3. Updated file: `sop/before-shell-execution-hook-spec.md` (v14589)

New Check 11: **browser-automation preferred when `oci` CLI unavailable**

- **WARN** (not DENY) — nudges the agent without blocking.
- Matches: `oci` CLI invocations, `curl https://cloud.oracle.com/*`, `ORACLE_*` env vars, `~/.oci/config`.
- Override: `--no-browser-automation-check`.
- Synthetic test stub added: `TestBeforeShell_BrowserAutomationPreferred`.

Background:
- `oci` CLI is not installed on wsl1.
- OCI private key lives on retired MacBook (`/Users/jason.lian/.oci/oci_api_key.pem`).
- Web UI scraping via `curl` is brittle when MFA is required.
- `agent-browser` (Vercel Labs) drives Playwright + Chromium with stable `@eN` refs.

### 4. Resolved carry-forward: CF-v14588-03

The locally-generated `~/.ssh/oracle_jump` keypair was redundant (never pushed to OCI). Decision:

1. **Delete** `/home/jaslian/.ssh/oracle_jump` and `/home/jaslian/.ssh/oracle_jump.pub`.
2. **Archive** MacBook-era pubkey in 1Password `bsqycxxs2hxqyjiemxea7m47ae` (field `public_key`, fingerprint `xQwBn5ANaEIxNNlz5BSBSYfy+FVf/MEn1OMAtmdLbcQ`).
3. **Push** `~/.ssh/fleet_lan.pub` (fingerprint `4ngaUT06CcIJeplKVMvXD58XB0Y8u7CdzIXYiJP8xjs`) to `oracle-jump` once CF-v14588-01 is actioned.

Audit: `cf-v14588-03-resolution.md` (sha-256 fingerprints captured before deletion).

---

## Carry-forwards

| ID | Description | Severity | Owner | Sprint target |
|----|-------------|----------|-------|---------------|
| CF-v14588-01 | OCI jumps require operator login + MFA to recover SSH access | **BLOCKER** for fleet-mesh testing | operator | next available |
| CF-v14588-02 | Tailscale auth keys for wsl1_win11 / wsl2_win11 expired 2026-06-27 | HIGH | v14591 | v14591 |
| CF-v14588-04 | OCI API private key in 1Password `t6m7idvh5sipb3ubpcpnuhqvi4` is on retired MacBook — re-key via OCI console | MEDIUM | next available | n/a |
| **CF-v14589-01** | Once CF-v14588-01 is actioned, verify `oracle-jump` SSH works and `oci-sydney-jump` is online; update `evidence/v14588-oci-jump/README.md` with success | LOW | operator / v14590 | v14590 |

---

## Cross-references

- `sop/oracle-cloud-web-ui-automation.md` — new SOP (this sprint).
- `sop/oracle-cloud-tailscale-jump-server.md` — updated SOP (this sprint).
- `sop/before-shell-execution-hook-spec.md` — updated hook spec (this sprint).
- `evidence/v14588-oci-jump/README.md` — v14588 audit.
- `evidence/v14589-oci-sop/cf-v14588-03-resolution.md` — CF-v14588-03 audit.

---

## What was delivered in this sprint

1. ✅ Created `sop/oracle-cloud-web-ui-automation.md` (7.5 KB) — 3 self-service Playbooks.
2. ✅ Updated `sop/oracle-cloud-tailscale-jump-server.md` (multiple edits, ~30 lines changed).
3. ✅ Updated `sop/before-shell-execution-hook-spec.md` (added Check 11 + synthetic test stub).
4. ✅ Resolved CF-v14588-03 (deleted redundant `~/.ssh/oracle_jump` keypair, audit captured).
5. ✅ Updated CF tracker (CF-v14588-01 still open, CF-v14589-01 added).
