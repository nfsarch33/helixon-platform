# v14590 — SprintBoard ticket summary

Generated: 2026-07-10T07:30:35.381454+10:00

Total tickets: **17**

| # | Category | Item | UUID | Vendor URL |
|---|----------|------|------|------------|
| 1 | rotation-search-key | Rotate search API key: Perplexity embedding API key - keynear@hotmail.com | `zzvrihqejvqivwswlsbncbtfda` | https://www.perplexity.ai/settings/api |
| 2 | rotation-email-key | Rotate email API key: Brevo info@oztac 300 daily limit | `5won46qifska3q24i5sqhpqlbq` | https://app.brevo.com/settings/keys/api |
| 3 | rotation-email-key | Rotate email API key: Resend xiuyingni81@gmail.com | `23ad5gtlh7yxz7hu2xcuvo2ewu` | https://resend.com/api-keys |
| 4 | rotation-telegram-token | Rotate telegram API key: Telegram Bot - Cursor WSL1 | `7plsotwmnuc4s3kevyvstaoqua` | https://t.me/BotFather |
| 5 | rotation-telegram-token | Rotate telegram API key: Telegram Bot - Fleet Agent 1 | `gbqnlvhkop6lfsx4czf5gp6nga` | https://t.me/BotFather |
| 6 | rotation-email-key | Rotate email API key: Resend info@oztac | `nruynfsbnmjmqx6tld6c7uaopm` | https://resend.com/api-keys |
| 7 | rotation-search-key | Rotate search API key: zd api gateway openai models | `kocor3kayl7lsteqecmxpsue2u` | https://platform.openai.com/api-keys |
| 8 | rotation-search-key | Rotate search API key: minimax-api-2 | `hblg7fxbhxnlmnv3us3i6jo2wu` | https://platform.minimax.io/user-center/billing/api-key |
| 9 | rotation-email-key | Rotate email API key: Brevo info@oztac 300 daily limit | `qqt3bayjxxddc6hapn6tmtk5j4` | https://app.brevo.com/settings/keys/api |
| 10 | rotation-search-key | Rotate search API key: minimax-api-1 | `ripotpfq43jzlreor4zo2ay734` | https://platform.minimax.io/user-center/billing/api-key |
| 11 | rotation-email-key | Rotate email API key: Resend jaslian@gmail.com | `pwjkp2gii6cnaqwwj4fmesdxd4` | https://resend.com/api-keys |
| 12 | rotation-email-key | Rotate email API key: Resend Login jzlian083@gmaifl.com | `reg7i5k2duyn6ra4wjodka2jiq` | https://resend.com/api-keys |
| 13 | rotation-search-key | Rotate search API key: Exa API Key 1 | `pwfsbfuvhqaz63oc7ujgb24uou` | https://dashboard.exa.ai/api-keys |
| 14 | rotation-telegram-token | Rotate telegram API key: Telegram Bot - Fleet Agent 2 | `czdbviw37zsfk7e23clly2bvw4` | https://t.me/BotFather |
| 15 | rotation-search-key | Rotate search API key: Tavily API Key 1 | `gmfbh6yqhrgcovmxmbhbttl3cu` | https://tavily.com/api-keys |
| 16 | rotation-email-key | Rotate email API key: Resend keynear@gmail.com | `mjlr5fkdjj3mhd2c3a7t4zzroe` | https://resend.com/api-keys |
| 17 | rotation-email-key | Rotate email API key: SMTP2GO | `mqveimv2o5z4kiqnjybepiwbpq` | https://app.smtp2go.com/settings/apikeys/ |

## Import

To import these tickets into SprintBoard:

```bash
# When SprintBoard API is responsive (current health probe hangs):
curl -X POST http://localhost:9400/api/v1/tickets/import \
  -H 'Content-Type: application/x-ndjson' \
  --data-binary @/home/jaslian/Code/helixon-platform/evidence/v14590-rotation/sprintboard-tickets.ndjson
```

If API still hangs, import via SQLite directly:
```bash
sqlite3 ~/.config/helix-dev-tools/sprintboard.db < import.sql
```
