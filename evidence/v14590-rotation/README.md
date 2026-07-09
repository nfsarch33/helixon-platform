# v14590 — Rotate non-excluded rotatable items (CF-v14573-01 part 2)

**Date**: 2026-07-10 (UTC+10)
**Sprint**: v14590 (Pair 8 MVP)
**Plan**: Rotate 8 email + 3 Telegram + 5 search/embedding = 16 items; actual count = 17.

---

## TL;DR

- ✅ Inventoried all 85 items in `Cursor_IronClaw` vault.
- ✅ Captured SHA-256 fingerprints for **17/17 rotatable items** (1 more than plan).
- ✅ Generated **17 SprintBoard tickets** (NDJSON for import — SprintBoard API health probe is currently hanging, so direct DB import may be needed).
- ✅ Mapped rotation URLs for each vendor.
- ✅ Identified affected services: only `engramd` (via `minimax-api-1`/`minimax-api-2` for `ENGRAM_EMBED_KEY`).
- ⏭️ Deferred to operator: actual key rotation requires vendor console MFA (cannot be automated from wsl1).

---

## Rotatable items (17 total)

### Email vendor keys (8)

| Vendor | Item | UUID | Field | Old SHA-256 (truncated) |
|--------|------|------|-------|--------------------------|
| Brevo  | info@oztac 300 daily limit #1 | `5won46qifska3q24i5sqhpqlbq` | apiKey | `sha256:ec1b8afac0a4f8766...` |
| Brevo  | info@oztac 300 daily limit #2 | `qqt3bayjxxddc6hapn6tmtk5j4` | apiKey | `sha256:ec1b8afac0a4f8766...` |
| Resend | info@oztac | `nruynfsbnmjmqx6tld6c7uaopm` | apiKey | `sha256:cda368fe58cfc1343...` |
| Resend | jaslian@gmail.com | `pwjkp2gii6cnaqwwj4fmesdxd4` | apiKey | `sha256:5ef16c269962d8cc9...` |
| Resend | keynear@gmail.com | `mjlr5fkdjj3mhd2c3a7t4zzroe` | apiKey | `sha256:804b580bfcf11f3f8...` |
| Resend | xiuyingni81@gmail.com | `23ad5gtlh7yxz7hu2xcuvo2ewu` | apiKey | `sha256:c327e3acbbd152e8d...` |
| Resend | Login jzlian083@gmaifl.com | `reg7i5k2duyn6ra4wjodka2jiq` | apiKey | `sha256:4ef9a2e4a740b8ab2...` |
| SMTP2GO | (retired per v14271-04) | `mqveimv2o5z4kiqnjybepiwbpq` | apiKey | `sha256:c242747129b015438...` |

### Telegram bot tokens (3)

| Bot | UUID | Field | Old SHA-256 (truncated) |
|-----|------|-------|--------------------------|
| Telegram Bot - Cursor WSL1 | `7plsotwmnuc4s3kevyvstaoqua` | password | `sha256:8dfce3a3cd5c7bb0e...` |
| Telegram Bot - Fleet Agent 1 | `gbqnlvhkop6lfsx4czf5gp6nga` | password | `sha256:2ce61c053f5091038...` |
| Telegram Bot - Fleet Agent 2 | `czdbviw37zsfk7e23clly2bvw4` | password | `sha256:bc562f4fbfcfae6a7...` |

### Search / embedding keys (6)

| Service | Item | UUID | Field | Old SHA-256 (truncated) |
|---------|------|------|-------|--------------------------|
| Perplexity | embedding API key - keynear@hotmail.com | `zzvrihqejvqivwswlsbncbtfda` | credential | `sha256:f5414545a6fec9226...` |
| Tavily | API Key 1 | `gmfbh6yqhrgcovmxmbhbttl3cu` | credential | `sha256:b3e7fd851dd6a5083...` |
| Exa | API Key 1 | `pwfsbfuvhqaz63oc7ujgb24uou` | credential | `sha256:e3a95da22acdb4e43...` |
| MiniMax | api-1 | `ripotpfq43jzlreor4zo2ay734` | api-key | `sha256:be6bbbd91206bf75c...` |
| MiniMax | api-2 | `hblg7fxbhxnlmnv3us3i6jo2wu` | api-key | `sha256:f1492b2c6aa3e2b96...` |
| OpenAI (zd gateway) | api key in notesPlain | `kocor3kayl7lsteqecmxpsue2u` | OPENAI_API_KEY_in_notesPlain | `sha256:f7b95c88888884bb8...` |

### Excluded (per CF-v14573-01)

| UUID | Item | Reason |
|------|------|--------|
| `bsqycxxs2hxqyjiemxea7m47ae` | oracle_jump SSH Key | MacBook-era; already archived (CF-v14588-03 resolved v14589) |
| `55z2jgso2aefsu6ropoxiip4by` | MSI Win PC WSL Ubuntu Login | DO NOT ROTATE per operator directive 2026-07-10 |
| `vygyluc6uiuhqj4kee4ygy527m` | wsl1_win11 tailscale auth key | Rotation deferred to v14591 |
| `s2h64ocrkfye2fr4lszgwcbijm` | wsl2_win11 tailscale auth key | Rotation deferred to v14591 |
| `nrmcadhudzunh37ajvoqfsmmyy` | tailscale-auth-key (reusable) | Rotation deferred to v14591 |
| `7w7iwwwkx55u27qcddr6prwv4m` | 1password op Service Account Auth Token | Hard exclude (would lock us out) |

---

## SprintBoard tickets (17 generated)

- File: `sprintboard-tickets.ndjson` (NDJSON, ready for import).
- Summary: `tickets-summary.md`.
- **Import blocker**: `sprintboard-cli health` and `curl /healthz` hang indefinitely. SprintBoard API is non-responsive. Tickets are ready for import once API recovers (CF-v14590-01).

---

## Affected services

Only **`engramd`** (and by extension `engram.service` on wsl1) directly consumes any of the rotatable secrets — specifically `minimax-api-1` / `minimax-api-2` via the `ENGRAM_EMBED_KEY` env var.

### Post-rotation playbook for `engramd`

```bash
# After updating minimax-api-1 / minimax-api-2 in 1Password:
/home/jaslian/local/bin/secrets-bootstrap --service engramd --out /run/user/$(id -u)/engramd-env
systemctl --user restart engram.service
journalctl --user -u engram.service -n 20
# Verify: engram embedding endpoint responds (curl http://localhost:<engramd_port>/v1/embeddings)
```

### Other services (no action needed)

| Service | Affected by rotation? |
|---------|----------------------|
| fleet-agent | No — uses local stub credentials |
| llm-router | No — uses local model paths |
| svcregistryd | No — reads registry.yaml |
| sprintboard-api | No — reads SQLite db |

---

## Carry-forwards

| ID | Description | Severity | Owner | Sprint target |
|----|-------------|----------|-------|---------------|
| CF-v14590-01 | SprintBoard API `/healthz` hangs; 17 tickets in `sprintboard-tickets.ndjson` waiting for import | MEDIUM | operator / next sprint | v14591 |
| CF-v14590-02 | Actual rotation of 17 items requires operator at vendor consoles (MFA) | **HIGH** | operator | rolling (target: 30 days) |
| CF-v14590-03 | SMTP2GO key (`mqveimv2o5z4kiqnjybepiwbpq`) is RETIRED per v14271-04 — should be deleted from 1Password rather than rotated | LOW | operator | v14591 |

---

## What was delivered in this sprint

1. ✅ Inventoried all 85 items in `Cursor_IronClaw` vault (`/tmp/v14590_classification_v2.json`).
2. ✅ Captured SHA-256 fingerprints for all 17 rotatable items (`rotation-audit.csv`).
3. ✅ Mapped rotation URLs to each vendor (`rotation-plan.md`).
4. ✅ Generated 17 SprintBoard tickets ready for import (`sprintboard-tickets.ndjson`).
5. ✅ Identified affected service: `engramd` (only one) and documented post-rotation playbook.
6. ✅ Verified SMTP2GO is still in vault (retired per v14271-04; carry-forward to delete in v14591).

---

## Operator workflow (per item)

```
1. Open 1Password web vault: https://start.1password.com/vaults/Cursor_IronClaw
2. Search for the item UUID (or title)
3. Open the vendor rotation_url (above)
4. Generate a new key (or revoke + re-create for Telegram)
5. Update the 1Password item field with the new value
6. Re-run: python3 v14590_capture_fingerprints.py --verify --uuid <UUID>
7. The CSV row's new_sha256 column will be filled in
8. (If affects engramd) Re-run secrets-bootstrap + restart engramd
```

