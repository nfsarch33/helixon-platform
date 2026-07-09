# v14579 — 1Password rotation prep deep-dive (CF-v14573-01 part 1)

## Sprint goal

Per `v14576-v14593_deferred_cf_+_production-ready_27dc4bb3.plan.md` Pair 2 Review, validate the
v14573 prep runbook end-to-end:

1. Verify all 11 hard-excluded items are still present and have stable passwords (no silent rotation
   since v14573).
2. Run the runbook on a non-excluded item (Tavily API Key 1) — full lifecycle, NO actual rotation,
   only SHA-256 fingerprint logging.

## Phase 1 — Hard exclusion verification (11 items)

All 11 hard exclusions from the v14573 runbook were re-fetched by UUID. **0 missing**.

| UUID     | Title                                                       | Account           | PW len | sha256 (first 16) |
|----------|-------------------------------------------------------------|-------------------|--------|-------------------|
| pdueu5k...  | Win PC WSL Ubuntu Login GB                                 | jaslian@win1      | 16     | `be66f76d...`     |
| smvoqaxt...  | Win PC WSL Ubuntu Login GB / jason@win2                    | jason@win2        | 16     | `be66f76d...`     |
| sjhxjryiv...  | Win PC WSL Ubuntu Login GB / jason@win4                    | jason@win4        | 16     | `be66f76d...`     |
| 55z2jgso...  | MSI Win PC WSL Ubuntu Login                                | jason@msi         | 28     | `89b98665...`     |
| 7w7iwwwkx...  | 1password op Service Account Auth Token: win1/wsl1         | sa-win1           | 852    | `3af201c2...`     |
| mo6x2g5f...  | Service Account Auth Token: msi-win11-laptop               | sa-msi            | 852    | `644f7f45...`     |
| pfhiyhwiy...  | Service Account Auth Token: cursor_ironclaw                | sa-vault          | 852    | `82646444...`     |
| nrmcadhu...  | tailscale-auth-key                                         | tailnet-admin     | 0      | (stored in `notesPlain`, not password field) |
| jgt7yaix...  | Main Tailscale API Access token                            | ts-api            | 60     | `83a2b331...`     |
| vygyluc6...  | wsl1_win11 tailscale auth key expire on 27 Jun 2026        | wsl1-replacement  | 62     | `614370b3...`     |
| s2h64ocr...  | wsl2_win11 tailscale auth key expire on 27 Jun 2026        | wsl2-replacement  | 62     | `2e3b2593...`     |

**Findings**:
- All 4 Win PC logins (win1, win2, win4, msi) share the operator-specified GB password (16 chars,
  sha256 `be66f76d...`). Confirmed against the v14573 baseline.
- MSI login has a distinct 28-char password (operator worked from China on that machine, different
  account context).
- All 3 service account tokens are intact.
- Tailscale keys show their full value (62 chars) — those are *replacement* keys, scheduled to
  expire 2026-06-27; rotation deferred to v14591.
- `tailscale-auth-key` has no `password` field; value is in `notesPlain`. This is fine for the audit
  (we never rotate this).

## Phase 2 — Runbook drill on Tavily API Key 1

Picked a non-excluded rotatable item (Tavily API Key 1, `gmfbh6yqhrgcovmxmbhbttl3cu`).

**Drill steps performed (per `sop/1password-rotation-procedure.md` §Procedure)**:
1. ✅ Created evidence folder + drill audit (SprintBoard ticket would be created when authorized).
2. ✅ Pulled current value via `op item get --vault Cursor_IronClaw --format=json`.
3. ✅ Logged SHA-256 fingerprint: `b3e7fd851dd6a50835c2454d697c73a26a68e06ee53d7601c6cf3e013377aea0`
   (length 58 chars, prefix `tvly-d...`).
4. ✅ Wrote `audit.csv` row with `rotation=drill-no-rotation-old=new`.
5. ✅ Saved `tavily-pre-drill.json` for re-verification.
6. ❌ NO actual rotation performed — drill only. **Old == New sha256 by design**.

**Lesson learned**: Tavily's API key is stored in the `credential` field (not `password`). The
v14573 runbook should add a note about API keys vs login passwords — for vendor APIs the field
label is usually `credential` or `api_key`, not `password`. Fixed in this drill via runtime field
detection; the v14573 runbook should be amended in a future sprint (carry-forward CF-v14579-01).

## Files

- `audit.csv` — 12 rows: 11 hard exclusions + 1 drill item (Tavily)
- `drill-summary.json` — JSON summary of phases 1 + 2
- `tavily-pre-drill.json` — full op item JSON for Tavily API Key 1

## CF-v14573-01 status

**Part 1 complete** (prep deep-dive + drill). Remaining work:
- **CF-v14591-01** (new): `sop/1password-rotation-procedure.md` should be amended to cover the
  `credential` vs `password` field naming for vendor API tokens.

## Verification

- [x] All 11 hard exclusions still present, no silent rotation
- [x] Drill on a non-excluded item completed end-to-end (no actual rotation)
- [x] Audit trail in `audit.csv` with sha256 fingerprints (no raw passwords)
- [x] CF-v14579-01 carry-forward documented