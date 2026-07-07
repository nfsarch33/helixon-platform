# v16712-6: True LRU Refactor (v16711-LRU-1)

**Sprint**: v16712 Sprint 3
**Date**: 2026-07-07
**Status**: GREEN (10/10 selectKey tests)

## Problem (v16711 carry)

The v16711 verification surfaced that `selectKey` was **order-based** rather
than **timestamp-based**: it returned the first active key in config-declaration
order, not the key with the oldest `LastUsedAt`. Stable keys at the top of the
config were always picked until they hit the 60s cooldown.

## Fix

Refactored `selectKey` in `tools/email-vendor-rotation/main.go` to:

1. Iterate ALL active keys for the vendor (not just take the first)
2. Skip keys within the cooldown window
3. Track the key with the oldest `LastUsedAt` (true LRU)
4. **Never-used keys** (`LastUsedAt == ""`) win over any used key, with
   declaration-order tiebreaker for determinism

## TDD evidence

7 new RED tests in `selectkey_lru_test.go`:
- `TestSelectKey_TrueLRU` (picks oldest of 3 active keys; RED before fix)
- `TestSelectKey_TrueLRU_NeverUsedBeatsOldest` (RED before fix)
- `TestSelectKey_TrueLRU_SkipsCooldown` (PASS already)
- `TestSelectKey_TrueLRU_RespectsStatus` (PASS already)
- `TestSelectKey_TrueLRU_EmptyReturnsNil` (PASS already)
- `TestSelectKey_TrueLRU_AllCooldownReturnsNil` (PASS already)
- `TestSelectKey_TrueLRU_ForbiddenVendor` (PASS already)

After refactor: **all 10 selectKey tests pass** (3 pre-existing + 7 new).

## End-to-end verification

```bash
$ ./bin/email-vendor-rotation send --dry-run \
    --config tools/email-vendor-rotation/config.yaml \
    --subject "v16712 LRU verify" \
    --idempotency-key "v16712-lru-verify-001" \
    --job-id "v16712-lru-test"

{
  "alias": "resend-jaslian",
  "event": "email_vendor_rotation_attempt",
  "result": "dry-run",
  "vendor": "resend"
}
```

Real config: 1 active resend key (`resend-jaslian`), 4 standby. The active
key is correctly picked. With a 5-active-keys config the LRU behaviour would
be observable.

## Carry

- v16711-LRU-1: **CLOSED**
- Ready for v16712 closeout

## Files changed

- `helixon-platform/tools/email-vendor-rotation/main.go` (selectKey refactor; +30/-12 LOC)
- `helixon-platform/tools/email-vendor-rotation/selectkey_lru_test.go` (NEW; 7 tests)