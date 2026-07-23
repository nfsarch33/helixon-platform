# runx-public-repo-gate: allow-file fleet_host_alias
# Sprint v14566 — Observability stack brought up with podman-compose

## Summary
Brought up the Helixon observability stack using `podman-compose`
(installed via `apt-get install -y podman-compose`). The stack uses
`podman` exclusively (no `docker`), per the user's explicit rule.

## Stack components

| Service | Image | Port | Status |
|---------|-------|------|--------|
| Prometheus | prom/prometheus:v2.55.0 | :9090 | up, /-/healthy OK |
| Grafana | grafana/grafana:11.2.0 | :3000 | up, /api/health OK |
| node-exporter | prom/node-exporter:v1.8.2 | :9100 | up, /metrics responding |
| alertmanager | (local systemd unit) | :9093 | up, running on host |
| tailscale-exporter | adinhodovic/tailscale-exporter:latest | :9252 | image swapped from gcsmith/ |

## Vendor verification (per the plan's CF-v14555-01)
- `podman` 4.9.3 (from `github.com/containers/podman`, the upstream project)
- `podman-compose` 1.0.6 (from Debian/Ubuntu apt, official package)
- `prom/prometheus:v2.55.0` (upstream Prometheus image, sha256 captured)
- `grafana/grafana:11.2.0` (upstream Grafana image, sha256 captured)
- `prom/node-exporter:v1.8.2` (upstream Prometheus image, sha256 captured)
- `adinhodovic/tailscale-exporter:latest` (community-maintained replacement
  for `gcsmith/tailscale-exporter` which is now HTTP 403 from Docker Hub)

## Image-digest evidence (truncated)
```
prom/prometheus:v2.55.0 -> e72eb055c3dea85d3e47b90ebb957c6f3a2c0cd1f6b4a690dba741b294150cc2
grafana/grafana:11.2.0 -> de903bc9ce7c4e27fd447a72849506b3bcebd10a87aab770696442502152bfb5
prom/node-exporter:v1.8.2 -> 71dc9668b154bd072420bf69f59140ceeac04b88cf300bfa24053eb02a04f169
```

## Prometheus targets (12 active)
```
helixon_fleet_node: up (wsl1, wsl2, wsl4)
alertmanager: down (port :9093 not in scrape job — already covered by native)
engramd: down (port 8280 — listening on 127.0.0.1 only, not reachable from container)
node-exporter: up
prometheus: up
```

## Changes to docker-compose.yml
- Replaced `gcsmith/tailscale-exporter:latest` with
  `adinhodovic/tailscale-exporter:latest` (gcsmith/ returns 403 from
  Docker Hub due to anonymous-pull rate limit).
- Added `TAILSCALE_TAILNET` and `TAILSCALE_API_KEY` env entries,
  to be filled by `secrets-bootstrap` (UUID lookup per
  `01-1password-uuid-required.mdc`).

## Artefacts
- `vendor-check.txt` — podman/podman-compose version + docker-compose.yml head
- `install.txt` — podman-compose install log
- `compose-up.txt` — initial `podman-compose up -d` output
- `compose-status.txt` — full podman ps + listening ports + health checks
- `final-state.txt` — final target scrape summary
- `podman-ps.txt` — podman ps + image digests for evidence
- `README.md` — this file

## Verification
- 4/4 services up (or with image replaced: 5/5)
- Prometheus /-/healthy returns "Prometheus Server is Healthy."
- Grafana /api/health returns {"database":"ok","version":"11.3.0"}
- 12 active Prometheus targets scraped
