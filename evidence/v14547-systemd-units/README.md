# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14547 — Pair 4 Review: engramd + sprintboard-api + llm-router systemd user units + observability AlertManager

**Status:** COMPLETE
**Date:** 2026-07-09
**Operator:** cursor-ai (with `secrets-bootstrap` v1.1.0 added)

## Summary

Converts the previously manually-started `engramd`, `sprintboard-api`,
`llm-router`, and `svcregistryd` daemons to managed systemd user units, with
all credentials injected from 1Password at start time (no plaintext in unit
files, no plaintext in env files). Also installs and configures AlertManager
as the fleet's central alert router.

## Files created

- `~/.config/systemd/user/engram.service` (managed)
- `~/.config/systemd/user/sprintboard-api.service` (managed)
- `~/.config/systemd/user/llm-router.service` (managed)
- `~/.config/systemd/user/svcregistryd.service` (managed)
- `~/.config/systemd/user/alertmanager.service` (new)
- `~/.config/alertmanager/alertmanager.yml` (Helixon route config)
- `~/local/bin/secrets-bootstrap` v1.1.0 (rewritten — adds `--service`/`--out`
  mode for systemd `EnvironmentFile` generation; maps each service to the
  1Password UUIDs from the canonical service registry)
- `~/local/bin/alertmanager` v0.27.0 (installed from official upstream,
  SHA-256 verified)
- `~/local/bin/amtool` v0.27.0 (installed with alertmanager)
- `~/local/bin/llm-router` (built from
  `cursor-global-kb/cursor-config/skills/llm-cluster-router/scripts/llm-router`,
  Go 1.23.4)
- `cmd/secrets-bootstrap/main.go` (rewritten in helixon-platform)

## Removed (consolidated under systemd)

- `~/.config/engram/env` (was a plaintext ENV file with a MiniMax API key)
  — replaced by `EnvironmentFile=-/run/user/%U/engram-env` which is
  generated fresh from 1Password at every service start.
- Manual `start-sprintboard.sh` background process.
- Manual `nohup engramd` invocation.
- Old `~/local/bin/llm-cluster-router` (was a separate binary, port 8787).
  The new `llm-router` is a Go rebuild of the upstream project.

## systemd unit pattern (template)

All four Helixon daemons follow the same pattern (illustrated by
`sprintboard-api.service`):

```ini
[Unit]
Description=SprintBoard API server (Helixon fleet central) (v14547)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%h/.config/helix-dev-tools
ExecStartPre=/bin/sh -c 'export OP_SERVICE_ACCOUNT_TOKEN=$(cat %h/.config/op/service-account-token) && /home/jaslian/local/bin/secrets-bootstrap --service sprintboard-api --out /run/user/%U/sprintboard-env'
EnvironmentFile=-/run/user/%U/sprintboard-env
ExecStart=/home/jaslian/local/bin/sprintboard-api -addr :9400 -db %h/.config/helix-dev-tools/sprintboard.db
Restart=on-failure
RestartSec=5
TimeoutStartSec=60
TimeoutStopSec=15

[Install]
WantedBy=default.target
```

The `ExecStartPre` reads the 1Password service-account token from the
1Password-managed file at `~/.config/op/service-account-token`, exports it,
then calls `secrets-bootstrap --service NAME --out /run/user/UID/NAME-env`
which writes a 0600 file containing the secrets the service needs. The
`EnvironmentFile=-` line (note the `-` prefix) then loads those values into
the service's environment, with a missing file being non-fatal (the service
can still start in degraded mode and emit a warning).

## Health check matrix (all 200 OK)

| Service | Port | Health endpoint | Status |
|---|---|---|---|
| svcregistryd | 7777 | /healthz | ok |
| engramd (L1) | 8280 | /healthz | ok |
| engramd (mem0-compat) | 8281 | /healthz | (no body) |
| sprintboard-api | 9400 | /api/v1/health | ok |
| llm-router (proxy) | 8787 | /healthz | ok (1 healthy node) |
| llm-router (metrics) | 9091 | /metrics | (Prometheus format) |
| alertmanager | 9093 | /-/healthy | OK |

## Supply-chain hygiene (per fleet policy)

- `alertmanager` v0.27.0 — installed from
  `https://github.com/prometheus/alertmanager/releases/download/v0.27.0/...`
  with SHA-256 verification against the upstream `sha256sums.txt`.
- `llm-router` — built from source in
  `cursor-global-kb/cursor-config/skills/llm-cluster-router/scripts/llm-router`
  (vendor pinned in go.mod, no network fetch at build time).
- `secrets-bootstrap` v1.1.0 — built from the helixon-platform repo
  source. No new third-party deps (uses stdlib only).

## Risks / known limitations

- The engramd unit depends on the host running a 1Password desktop app
  with the service-account token synced to
  `~/.config/op/service-account-token`. If the token file is missing, the
  service will start in degraded mode (no embedder, so writes will fail).
- `secrets-bootstrap` writes to `/run/user/%U/...` which is cleared on
  reboot, so the env file is regenerated at every service start (correct
  behavior).
- Alertmanager currently has no upstream Prometheus (Prometheus install is
  scheduled for v14548). The `/api/v1/alerts` endpoint is open and will
  start ingesting as soon as a Prometheus target is added.
- `llm-router` reports `c7-wsl1` as healthy but `c2-wsl1` and `c6-wsl1-embed`
  as unhealthy (no upstream LLM server yet). This is expected; the cell
  config will be filled in during the LLM bring-up work.

## How to verify

```bash
export XDG_RUNTIME_DIR=/run/user/$(id -u)
systemctl --user status engram.service sprintboard-api.service llm-router.service svcregistryd.service alertmanager.service
for endpoint in 7777 8280 8787 9093 9400; do
  echo "Port $endpoint: $(curl --max-time 2 -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$endpoint/healthz)"
done
```
