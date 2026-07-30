#!/bin/bash
# runx-leak-scan: allow-file internal_ip
set -e
PAYLOAD='{"name":"fleet-agent-wsl1","host":"127.0.0.1","port":8686,"protocol":"http","status":"up","owner":"v14571","tailscale_ip":"100.84.108.92"}'
curl -fsS -X POST http://127.0.0.1:7777/api/v1/services \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD" \
  -w "\n--HTTP %{http_code}--\n" \
  >> "$HOME/logs/service-registry-register.log" 2>&1 \
  || echo "svcregistryd not ready, will retry on next login" >> "$HOME/logs/service-registry-register.log"
