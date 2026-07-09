# Sprint v14560 — svcregistryd live + svcregistry-bridge end-to-end

## Summary
`svcregistryd` is running on wsl1 at `127.0.0.1:7777` as a systemd
user service. `svcregistry-bridge` was run against the canonical
`inventory/services/registry.yaml` and registered 16 services into
the live daemon with **0 failures**.

## Live daemon state
```
$ systemctl --user status svcregistryd.service
Active: active (running) since Thu 2026-07-09 17:55:18 AEST; 4h 23min ago
   Main PID: 3143831 (svcregistryd)
   Memory: 4.3M
       CPU: 632ms
```

## Bridge run
```
$ ~/local/bin/svcregistry-bridge \
    --registry ~/Code/cursor-global-kb/inventory/services/registry.yaml \
    --api http://127.0.0.1:7777 \
    --owner v14560 \
    --timeout 15s
... 16 INFO msgs ...
bridge: registered=16 failed=0 skipped=0
exit=0
```

## Endpoints verified

| Endpoint | Method | Status | Notes |
|----------|--------|--------|-------|
| /healthz | GET | 200, body=`ok` | liveness probe |
| /api/v1/services | GET | 200, JSON | 16 services after bridge |
| /metrics | GET | 200, Prometheus text | 4 metric lines, includes `svcregistry_operations_total{op="register",status="ok"} 16` |

## Services registered (16 total)
1. engramd :8280
2. engramd-mem0compat :8281
3. sprintboard-api :9400
4. sprintboard-mcp :9401
5. llm-cluster-router :8787
6. llm-cluster-router-metrics :9091
7. llm-cluster-router-debug :6060
8. llama-server-c7-q8 :8010
9. llama-server-c2-q4 :8011
10. vllm-c1-awq :8004
11. ollama-embed-c6 :11434
12. prometheus :9090
13. grafana :3000
14. alertmanager :9093
15. k3s-server :6443
16. ssh-fleet-lan :22

## Bridge CLI flags confirmed
- `--registry` (path to YAML, default ok)
- `--api` (svcregistryd base URL, override from default `:9103` → `:7777`)
- `--owner` (stamps `v14560` on every registered service)
- `--timeout` (15s per HTTP request)
- `--dry-run` (would print what would be registered without POSTing)

## Artefacts
- `bridge-stdout.txt` — full bridge run output
- `services-list.json` — GET /api/v1/services response
- `metrics.txt` — Prometheus /metrics output
- `health.txt` — GET /healthz response
- `README.md` — this file

## Verification
- 16/16 services registered successfully (0 failures)
- /healthz returns "ok"
- /api/v1/services returns the expected 16 entries
- /metrics reports `svcregistry_operations_total{op="register",status="ok"} 16`
