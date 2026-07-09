# Sprint v14553 — Helixon Fleet Agent Service + Service Registry Loop

## Summary
Wired the Helixon fleet agent into systemd on wsl1, with an auto-register
step that closes the loop into the service registry created in v14540.

## Artefacts

### 1. Config
- `~/.config/helixon/wsl1-fleet-agent.yaml` — wsl1 fleet-agent
  configuration. Connects to:
  - LLM: `http://127.0.0.1:8080/v1` (llm-router, model `Qwen3.6-27B-MTP`)
  - SprintBoard: `http://127.0.0.1:9400`
  - Service registry bridge: `http://127.0.0.1:7777` (svcregistryd)
  - Canonical registry SOT: `/home/jaslian/Code/cursor-global-kb/inventory/services/registry.yaml`
  - SQLite session DB: `%h/.local/share/helixon/fleet-wsl1.db`
- `systemd-units/wsl1-fleet-agent.example.yaml` — checked into the
  repo as a reference for win2/wsl2, win3/wsl3, win4/wsl4.

### 2. Systemd unit
- `~/.config/systemd/user/fleet-agent.service` — runs `helixon serve`
  with the config above, on `127.0.0.1:8686`. Uses
  `secrets-bootstrap --service fleet-agent` to inject
  `OPENAI_API_KEY` (and any future fleet-agent creds) without ever
  touching plaintext config.
- `~/.config/systemd/user/service-registry-register.service` — a
  `Type=oneshot` unit that POSTs a JSON service descriptor to
  `http://127.0.0.1:7777/api/v1/services` once both
  `svcregistryd.service` and `fleet-agent.service` are up. This closes
  the loop: a fleet agent that comes online will *always* appear in
  the service registry.

### 3. Per-node template
- `~/.config/systemd/user/fleet-agent@.service.template` — usage notes
  for the future template unit that will allow
  `systemctl --user start fleet-agent@wsl2.service` etc.

### 4. Verification
- `systemctl --user status fleet-agent.service` →
  `Loaded: loaded / Active: inactive (dead)` (enabled, not started).
- `systemctl --user status service-registry-register.service` →
  `Loaded: loaded / Active: inactive (dead)` (enabled, not started).
- `helixon doctor --config ~/.config/helixon/wsl1-fleet-agent.yaml` →
  parses all fields, only fails on `OPENAI_API_KEY` which is expected
  (the secrets-bootstrap ExecStartPre will provide it at runtime).

## Notes
- The fleet-agent is not started as part of this sprint; that will be
  part of v14555 once the wsl1→wsl2/wsl3/wsl4 mesh is fully built and
  SprintBoard token plumbing is complete.
- The per-node variants (wsl2, wsl3, wsl4) will reuse this exact
  template with only the `Tailscale IP` in the provider section
  changed (the LLM router handles routing).