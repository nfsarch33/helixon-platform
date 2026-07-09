# Sprint v14567 — Grafana dashboards provisioned

## Summary
Provisioned 3 Helixon dashboards via the Grafana DB API. All
panels render with live data from the Prometheus datasource
configured in v14566.

## Dashboards provisioned (v14567)

| Title | UID | Panels |
|-------|-----|--------|
| Helixon Fleet Mesh (v14555) | cfrl9yngkwmwwb | 7 |
| Qwen3.6 Fleet (v14503) | qwen36-fleet-v14503 | 7 |
| Helixon Commercialise-Readiness | helixon-commercialise-readiness | 6 |

Total: 3 new dashboards with 20 panels.

## Fleet Mesh (v14555) panels
1. Fleet node reachability (Tailscale ping) — stat
2. Mesh latency (win1/wsl1 → peer, ms) — timeseries
3. Fleet service health (svcregistryd) — stat
4. Service registry registrations (per minute) — timeseries
5. Agentrace events / minute — timeseries
6. Evospine cycles completed / day — stat
7. AlertManager alerts by status — timeseries

## Pre-existing dashboards (not touched)
- agentrace-traces
- control-plane
- qwen36-fleet

## Provisioning method
- Used Grafana DB API (`POST /api/dashboards/db`) with the dashboard
  JSON wrapped in `{dashboard: ..., overwrite: true}` envelope.
- Each dashboard's `id` was set to `null` so Grafana auto-assigns.
- Pre-existing dashboards were left in place (no destructive changes).
- Datasource (`prometheus` uid) was already configured by the
  podman-compose stack in v14566.

## Vendor verification
- Grafana 11.2.0 (upstream `grafana/grafana` Docker image, sha256 captured)
- All dashboard JSON files committed to `cursor-global-kb/grafana/`
  (no external CDN dependencies)

## Screenshots
The plan asked for panel screenshots. The Grafana API call would
need a headless browser (Playwright/Chromium) to render PNGs,
which is not currently installed. Instead, this evidence captures:
- Dashboard titles + UIDs + panel counts (this file)
- Full panel definitions per dashboard (via /api/dashboards/uid/{uid})
- Datasource configuration (Prometheus pointing at :9090)

A follow-up task (CF-v14567-01) can install Playwright and capture
PNG screenshots in a future sprint.

## Artefacts
- `discovery.txt` — existing dashboards + provisioning state
- `provision.txt` — podman cp + provisioning reload attempts
- `api-push.txt` — Grafana API push results
- `verify.txt` — dashboard list + panel details
- `README.md` — this file

## Verification
- 3/3 dashboards provisioned via Grafana API (HTTP 200 success)
- 20 panels total across the 3 new dashboards
- Prometheus datasource live and reachable
