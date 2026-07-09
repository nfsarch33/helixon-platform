# v14591 — Tailscale auth key replacement procedure

**Date**: 2026-07-10T07:35:00+10:00
**Sprint**: v14591 (Pair 8 Review)
**Context**: CF-v14588-02 — two Tailscale auth keys expired on 2026-06-27.

## Expired keys

| UUID | Item | Status |
|------|------|--------|
| `vygyluc6uiuhqj4kee4ygy527m` | wsl1_win11 tailscale auth key | EXPIRED 2026-06-27 |
| `s2h64ocrkfye2fr4lszgwcbijm` | wsl2_win11 tailscale auth key | EXPIRED 2026-06-27 |

Raw audit: `tailscale-auth-keys-audit.json`.

## Replacement procedure (operator action)

> **Note**: this is a REPLACEMENT (new key generated, old key revoked), not a mid-arc rotation.
> Per the v14591 plan §Pair 8 Review: "the Tailscale auth keys that expire 2026-06-27
> (`vygyluc6uiuhqj4kee4ygy527m`, `s2h64ocrkfye2fr4lszgwcbijm`) — these are REPLACEMENT,
> not mid-arc rotation."

### Step 1: Generate new auth keys in Tailscale admin

1. Open https://login.tailscale.com/admin/settings/keys
2. Click "Generate auth key"
3. **Key 1 (for wsl1_win11)**:
   - Description: `wsl1_win11 tailscale auth key v14591-replacement`
   - Reusable: ✅ (for fleet-onboarding automation)
   - Expiry: 90 days (auto-revoke)
   - Tags: `tag:win`, `tag:tailscale-auth`
4. **Key 2 (for wsl2_win11)**:
   - Description: `wsl2_win11 tailscale auth key v14591-replacement`
   - Reusable: ✅
   - Expiry: 90 days
   - Tags: `tag:win`, `tag:tailscale-auth`
5. Copy both keys (each is `tskey-auth-...`)

### Step 2: Update 1Password items

```bash
# Key 1 (wsl1)
op item edit vygyluc6uiuhqj4kee4ygy527m \
  --vault Cursor_IronClaw \
  password="<NEW_KEY_1>" \
  "notesPlain=wsl1_win11 tailscale auth key. Generated 2026-07-10 (v14591 replacement). Previous key expired 2026-06-27. Expiry: 2026-10-08 (90 days)."

# Key 2 (wsl2)
op item edit s2h64ocrkfye2fr4lszgwcbijm \
  --vault Cursor_IronClaw \
  password="<NEW_KEY_2>" \
  "notesPlain=wsl2_win11 tailscale auth key. Generated 2026-07-10 (v14591 replacement). Previous key expired 2026-06-27. Expiry: 2026-10-08 (90 days)."
```

### Step 3: Re-authenticate fleet nodes

The new keys are not yet used by any active node (wsl1 and wsl2 are already auth'd via the older reusable key `nrmcadhudzunh37ajvoqfsmmyy`). The new keys are for **future re-onboarding** (e.g. if wsl1 or wsl2 gets reset and needs to rejoin the tailnet).

For now, no immediate action needed beyond updating 1Password.

### Step 4: Revoke old keys

In Tailscale admin → Keys → find the expired keys → click "Revoke" (they may already be auto-revoked by the expiry).

## Audit trail

| Action | Timestamp | Owner | Outcome |
|--------|-----------|-------|---------|
| Inventory expired keys | 2026-07-10 07:35 AEST | v14591 | Captured (audit JSON) |
| Generate new keys | TBD | operator | Pending |
| Update 1Password | TBD | operator | Pending |
| Revoke old keys | TBD | operator | Pending |

## Carry-forwards

| ID | Description | Severity | Sprint target |
|----|-------------|----------|---------------|
| CF-v14591-01 | Operator to generate new Tailscale auth keys + update 1Password | HIGH | v14591 (operator action) |
| CF-v14591-02 | Set 2026-10-08 calendar reminder for next auth key rotation | LOW | v14591 |
