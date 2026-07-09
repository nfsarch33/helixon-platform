# Sprint v14541 — helix-dev-tools fleet services CLI + service discovery

## Summary
Wired the user-facing `helix-dev-tools services ...` subcommands and
shipped the browser-based `inventory/services/ui/register.html`.

## Artefacts
- `helixon-platform/internal/cli/services_cmd.go`
- `helixon-platform/internal/cli/services_cmd_test.go`
- `cursor-global-kb/inventory/services/ui/register.html`

## Verification
- `helix-dev-tools services list` — 40+ services
- `helix-dev-tools services health` — all green
