# Sprint v14540 — Service Registry Schema + registra Go Binary

## Summary
Consolidated 6 fragmented data sources into a single canonical
`inventory/services/registry.yaml` SOT and built the `registra` Go
CLI to query it.

## Artefacts
- `cursor-global-kb/inventory/services/registry.yaml` — 40+ services
- `cursor-global-kb/inventory/services/registry.json` — JSON mirror
- `helixon-platform/internal/registra/registra.go` — data model
- `helixon-platform/internal/registra/registra_test.go` — TDD tests
- `helixon-platform/cmd/registra/main.go` — Cobra CLI
- `helixon-platform/internal/services/registry.go` — vendored copy

## Verification
- `go test ./internal/registra/...` — pass
- `registra list | wc -l` — 40+
