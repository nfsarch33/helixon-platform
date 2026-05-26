# Helixon Platform -- Claude Code / Cursor Agent Guidelines

This file is for AI coding agents (Claude Code, Cursor, etc.) working on this repo.

## Quick Reference

```bash
# Build
go build ./...

# Test (always use -race)
go test -race -cover ./...

# Lint
go vet ./...

# Run (no config needed for echo mode)
go run ./cmd/helixon repl
go run ./cmd/helixon doctor
```

## Rules

1. **TDD**: Write failing test first, then implementation.
2. **Race-free**: All code must pass `go test -race`.
3. **Coverage**: Maintain 75%+ coverage per package.
4. **No secrets**: Never commit credentials. Use `${ENV_VAR}` in YAML.
5. **Conventional commits**: `feat(fleet): add daily report generation`.
6. **Error wrapping**: Always wrap with context via `fmt.Errorf`.
7. **Structured logging**: Use `log/slog` with `slog.String("component", "...")`.

## Architecture

- `internal/helixon/` contains the runtime, channels, tools, memory, control plane
- `internal/llm/` contains provider abstraction (OpenAI-compat, Bedrock, mock)
- `internal/callbacks/` contains observability callbacks (NDJSON, OTel, Prometheus)
- `internal/evalfw/` contains the evaluation framework
- `cmd/helixon/` is the thin CLI binary

The `Runtime` struct owns the lifecycle. All sub-packages compose into it
via `ConfigOption` functions: `WithChannel`, `WithMemory`, `WithSprintboard`,
`WithAgentrace`.

## Testing patterns

- Use `httptest.NewServer` for HTTP endpoint tests
- Use `testify/assert` and `testify/require`
- Use `context.Background()` in tests
- Use `t.TempDir()` for file-system tests
- Use `Eventually` for async assertions (with 5s timeout, 10ms tick)
