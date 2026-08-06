# runx-public-repo-gate: allow-file fleet_host_alias,network_topology,secret_cred_ref
# Helixon Platform Runbook

> **Status:** MVP-ready (Sprint v16041 added 2026-07-05).
> **Owner:** cursor-parent (nfsarch33).
> **Audience:** operators running `helixon` in production.

This runbook is the on-call reference for `helixon-platform`. It covers
health verification, common failure modes, recovery procedures, and
escalation paths. Cross-reference [README](../README.md) for
architecture and [DEPLOYMENT.md](./DEPLOYMENT.md) for install/upgrade.

## 1. Health verification

### 1.1 Liveness probe (HTTP)

```bash
# Hit the agent's /healthz endpoint. Should return 200 with JSON body.
curl -fsS http://localhost:8080/healthz | jq .
```

Expected response (200 OK):

```json
{
  "status": "ok",
  "uptime_seconds": 12345,
  "agent_count": 3,
  "memory_l1_entries": 1234
}
```

If the request hangs for >5s, the agent runtime is unresponsive. See
§3.1 below.

### 1.2 Readiness probe

```bash
# /readyz returns 200 only when LLM endpoints + memory L1 are reachable.
curl -fsS http://localhost:8080/readyz | jq .
```

A 503 from `/readyz` means LLM provider or memory layer is unreachable.
Do NOT route traffic to this instance until `/readyz` returns 200.

### 1.3 Doctor

```bash
helixon doctor
```

Runs an 8-probe health check (config, memory, callbacks, channels,
agent loop, LLM provider, git hook, observability). Each probe
returns GREEN/YELLOW/RED.

### 1.4 Observability

NDJSON metrics stream to stdout in JSON-per-line format. For
long-running agents, prefer `--metrics-prometheus=:9091` to expose
Prometheus-format metrics on a dedicated port.

Key metrics to monitor:

- `helixon_agent_loop_iterations_total` (counter)
- `helixon_tool_dispatch_failures_total` (counter)
- `helixon_memory_l1_hit_ratio` (gauge)
- `helixon_channel_active_connections` (gauge)

## 2. Common failure modes

### 2.1 LLM provider 401/403

**Symptom:** Agent loop logs `provider auth failed` or `401 from upstream`.

**Diagnosis:**

```bash
helixon doctor --probe llm
```

**Recovery:**

1. Verify API key in env: `[ -z "$HELIXON_LLM_KEY" ] && echo unset || echo set`
 (NEVER print the value; see `no-shell-leak.mdc`).
2. If unset, source from 1Password: `op read 'op://Cursor_IronClaw/helixon-llm-key/credential' | envconsul reload`
 (use the `op` pipe pattern, never argv).
3. Restart agent: `systemctl --user restart helixon.service` (or
 `helixon serve` foreground process).

### 2.2 Memory L1 unreachable

**Symptom:** `memory.Search` returns error, agent falls back to L2 (git KB).

**Diagnosis:**

```bash
helixon doctor --probe memory
```

**Recovery:**

1. If Engram is the L1 backend, verify the tunnel: `runx tunnel status engram-tokyo`
2. If L1 is offline, the agent will degrade to L2-only. This is a
 degraded but **acceptable** state for up to 1 hour.
3. If L1 is permanently down, escalate to operator.

### 2.3 Channel disconnected (WebSocket / MCP stdio)

**Symptom:** Client disconnects repeatedly, agent logs `channel EOF`.

**Recovery:**

1. Check `helixon_channel_active_connections` — should be > 0.
2. Restart the channel: `helixon serve --channel=ws --port=8081`
3. If the channel fails to bind, check `lsof -i :8081` for orphan processes.

### 2.4 Tool dispatch failure

**Symptom:** Agent logs `tool X returned error code 500`.

**Diagnosis:**

1. Run `helixon doctor --probe callbacks` to see callback health.
2. Check tool-specific logs in `~/logs/runx/<tool>.ndjson`.

**Recovery:**

1. Most tool failures are transient (network blips). The agent retries
 up to 3 times with exponential backoff.
2. If a specific tool is failing >5 times/min, file an incident and
 disable the tool: `helixon config set tools.disabled=<tool-name>`.

## 3. Recovery procedures

### 3.1 Unresponsive agent runtime

```bash
# 1. Confirm unresponsive
curl -fsS --max-time 5 http://localhost:8080/healthz || echo "DOWN"

# 2. Capture stack trace (if Go pprof is enabled)
curl -fsS http://localhost:8080/debug/pprof/goroutine?debug=2 > /tmp/helixon-goroutines.txt

# 3. Send SIGTERM (graceful shutdown, 30s timeout)
kill -TERM $(pgrep -f 'helixon serve')

# 4. Wait 30s, then check exit
sleep 30
pgrep -f 'helixon serve' || echo "shutdown clean"

# 5. If still alive, SIGKILL
kill -9 $(pgrep -f 'helixon serve') 2>/dev/null

# 6. Restart
helixon serve --config=/etc/helixon/config.yaml
```

### 3.2 Corrupted memory state

If L1 (Engram / SQLite) becomes corrupted:

```bash
# 1. Stop the agent
systemctl --user stop helixon.service

# 2. Snapshot the corrupted DB
cp ~/.local/share/helixon/memory.db /tmp/helixon-memory-broken-$(date +%s).db

# 3. Re-initialize (LOSES LOCAL-ONLY memory; L2 KB survives)
helixon memory init --force

# 4. Restart
systemctl --user start helixon.service
```

The L2 git KB (`~/Code/cursor-global-kb`) is the durable source of
truth; L1 can always be rebuilt from L2.

### 3.3 Git hook failure (memory persistence)

If the `post-commit` hook fails to write to L1:

```bash
# 1. Verify hook is installed
ls -la .git/hooks/post-commit

# 2. Test the hook manually
git commit --allow-empty -m "test" && cat ~/.local/share/helixon/last-commit.txt

# 3. If hook is missing, re-install
helixon install-hooks --repo $(pwd)
```

## 4. Escalation paths

| Severity | Symptom | Action |
|----|----|----|
| P1 | Agent runtime down >5 min | Page operator, run §3.1 |
| P1 | Memory L1 + L2 both down | Page operator, file incident |
| P2 | Single tool failing >10/min | Disable tool, file ticket |
| P2 | LLM provider 5xx >5/min | Switch to backup provider via `--llm-fallback` |
| P3 | Single channel disconnected | Auto-reconnect; no action needed |
| P3 | Cosmetic UI issue | File ticket, no escalation |

## 5. Upgrade procedure

See [DEPLOYMENT.md](./DEPLOYMENT.md) §3 for upgrade steps.

## 6. References

- [README.md](../README.md) — architecture overview
- [DEPLOYMENT.md](./DEPLOYMENT.md) — install / upgrade
- `sop/helixon-platform-runbook.md` — global-kb cross-reference (planned)
- `helixon doctor --help` — built-in doctor tool
- `helixon metrics --help` — Prometheus exporter