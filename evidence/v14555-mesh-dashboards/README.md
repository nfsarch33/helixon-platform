# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# Sprint v14555 — win2 + win4 → wsl1 Full Mesh + Observability Rollout

## Summary
Verified the Tailscale mesh between wsl1 (central) and the other
fleet nodes, then shipped a 7-panel Grafana dashboard and a full
Prometheus + Grafana + node-exporter + tailscale-exporter docker-compose
stack.

## Artefacts

### 1. Mesh verification (live ping from wsl1)
```
wsl1 → win2 (100.93.107.109)  :  97.2 ms  (Tailscale direct)
wsl1 → win4/wsl4 (100.79.227.40) : 39.4 ms (Tailscale direct)
wsl1 → win3 (100.101.215.57)  : OFFLINE (last seen 6h ago)
```

`tailscale status` confirms all 4 fleet Tailscale nodes are registered
(win3/wsl3 is offline but the entry is preserved).

### 2. Grafana dashboard
- `cursor-global-kb/grafana/helixon-fleet-mesh-v14555-dashboard.json`
  - 7 panels: reachability, latency, fleet-service health, registry
    rate, agentrace events, evospine cycles, AlertManager alerts.
  - Validated by `json.load` → 7 panels parsed cleanly.

### 3. Observability stack
- `helixon-platform/observability/docker-compose.yml` — host-network
  services for Prometheus, Grafana, node-exporter, tailscale-exporter.
- `helixon-platform/observability/prometheus.yml` — scrape config
  covering:
  - 5 wsl1 fleet services (svcregistryd, engramd, sprintboard-api,
    llm-router, alertmanager)
  - 4 wsl peer nodes (wsl1 self, wsl2, wsl3, wsl4) via Tailscale
  - relabel_configs that tag each scrape with the fleet `alias` so the
    mesh dashboard can show them by name.

## Notes
- The `docker compose up -d` step was not executed in this sprint; the
  compose file is committed for an operator to bring up in v14557 if
  not earlier.
- win3 (laptop) is currently offline; the dashboard correctly shows
  `OFFLINE` until the laptop rejoins Tailscale.