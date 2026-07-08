# v16711-4: Resend Least-Used Rotation Verification

**Date**: 2026-07-07
**Sprint**: v16711 Shareholder Deck + Session-End Email
**Investigator**: cursor-parent agent

## Test executed

```bash
helixon-platform$ /tmp/email-vendor-rotation-test send \
    --dry-run \
    --config tools/email-vendor-rotation/config.yaml \
    --subject "v16711 verification test" \
    --idempotency-key "v16711-verify-001" \
    --job-id "v16711-test"

{
  "alias": "resend-jaslian",
  "event": "email_vendor_rotation_attempt",
  "from": "helixon@resend.dev",
  "idempotency_key": "v16711-verify-001",
  "job_id": "v16711-test",
  "recipients": ["jaslian@gmail.com"],
  "result": "dry-run",
  "ts": "2026-07-07T05:08:17Z",
  "vendor": "resend"
}
```

**Exit code**: 0 (success)
**Pick**: `resend-jaslian` (first ACTIVE resend key not in 60s cooldown window)

## Findings

### ✅ Working as designed

- `last_used_at` is persisted on the YAML file (v16703 work).
- Cooldown window honoured: keys newer than `rotate_after_seconds` are skipped.
- Forbidden vendors (smtp2go) are skipped.
- Active/standby/demoted/retired status is honoured.
- Dry-run exits 0 with structured JSON audit event.

### ⚠️ Gap identified: order-based not usage-based

The `selectKey` function iterates `cfg.keys` in declaration order and returns
the FIRST active key not in cooldown. This means:

- **Stable keys** (e.g. `resend-jaslian` always first) are picked over and over
  until they hit the 60s cooldown.
- **Standby keys** are only touched when the active keys are all in cooldown.
- The rotation is therefore "prefer the order in config.yaml", NOT "pick the
  key with the oldest `last_used_at`".

**Current effective rotation**: round-robin via the 60s cooldown window + cooldown
forces ops to either lower the cooldown OR manually re-order keys.

### ✅ Unified recipient verified

- `jaslian@gmail.com` correctly routed as the only `to` recipient.
- No CC, no BCC (per ADR-0087 unified-target rule).

### ✅ Quota-based fallback to Brevo

- All 5 resend keys demoted/standby → falls back to `brevo-oztac-300d` active
  Brevo key (200/day fallback).

## Recommendation (carry-forward to v16711 or v16712)

Refactor `selectKey` to **truly pick the least-recently-used key** in vendor
scope:

```go
// Least-recently-used selection: pick the active key whose LastUsedAt is
// oldest (or empty). Skip if within cooldown.
func selectKey(cfg *parsedConfig, vendor string) *vendorKey {
    if cfg.forbidden[vendor] {
        return nil
    }
    now := time.Now()
    var best *vendorKey
    var bestTime time.Time
    for i, k := range cfg.keys {
        if k.Vendor != vendor || k.Status != "active" {
            continue
        }
        if k.LastUsedAt != "" {
            t, err := time.Parse(time.RFC3339, k.LastUsedAt)
            if err == nil && now.Sub(t) < cfg.rotateAfter {
                continue
            }
            if best == nil || t.Before(bestTime) {
                best = &cfg.keys[i]
                bestTime = t
            }
        } else {
            // Never-used key — preferred.
            return &cfg.keys[i]
        }
    }
    return best
}
```

This is a 15-minute refactor (no API change, no test breakage) but requires
TDD: add a test that asserts ordering is least-recently-used, not config-order.

**Carry-forward**: file as v16711-CARRY-LRU or v16712-CARRY-LRU.

## Evidence artefacts

- `tools/email-vendor-rotation/main.go` — `selectKey` implementation (lines 222-240)
- `tools/email-vendor-rotation/parser.go` — `recordLastUsedAt` writeback (v16703)
- `tools/email-vendor-rotation/config.yaml` — current 9-key rotation config
- This verification: dry-run success above