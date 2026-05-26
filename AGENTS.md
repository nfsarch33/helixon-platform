# Helixon Platform -- Agent Guidelines

- Repo: `https://github.com/nfsarch33/helixon-platform`
- Module: `github.com/nfsarch33/helixon-platform`
- Language: Go (1.22+, tested on 1.25.6)

## Build & Test

```bash
go build ./...
go test -race -cover ./...
go vet ./...
```

## Run

```bash
# No config required
go run ./cmd/helixon doctor
go run ./cmd/helixon repl

# With config
go run ./cmd/helixon serve --config helixon.yaml --http-addr :8686
go run ./cmd/helixon platform --addr 127.0.0.1:8787
go run ./cmd/helixon task --config helixon.yaml --prompt "Hello"
```

## Coding Conventions

- Go, strict typing. `go vet` and `golangci-lint run` clean.
- No secrets in committed files. Use `${ENV_VAR}` expansion in YAML.
- Conventional commits: `type(scope): message`.
- TDD: write failing test first, then implementation.
- 75%+ test coverage required. Current: ~80% across all packages.
- Race-free: all tests must pass with `-race`.
- Error wrapping: use `fmt.Errorf("context: %w", err)` for all error returns.
- Structured logging: `log/slog` with component tags.

## Project Structure

```
cmd/helixon/           CLI entry point (serve, doctor, repl, platform, task)
internal/
  helixon/             Core runtime, lifecycle, channels, config, providers
    agent/             Agent loop + session store (SQLite + FTS5)
    builtins/          Built-in tools (shell, file read/write, web fetch)
    channel/           Channel adapters (MCP stdio, REPL, webhook)
    controlplane/      SprintBoard client, heartbeat monitor, A2A client
    dashboard/         HTTP dashboard views
    fleet/             Fleet task handler, delegation, reports, retry logic
    memory/            Engram, Mem0, workspace, hybrid memory
    platform/          HTTP/SSE platform server
    safety/            Cost guards, validation, sanitisation
    tooldispatch/      Tool registry, JSON Schema validation, agentrace
  callbacks/           Callback handlers (NDJSON, OTel, Prometheus, Mem0)
  evalfw/              Evaluation framework (suite runner, reporter)
  llm/                 LLM providers (OpenAI-compat, Bedrock, Claude CLI, mock)
```

## Key Types

| Type | Package | Purpose |
|------|---------|---------|
| `Runtime` | `helixon` | Agent lifecycle manager (Init → Configure → Run → Shutdown) |
| `RuntimeConfig` | `helixon` | YAML-driven configuration with defaults |
| `IncomingMessage` | `helixon` | Channel-agnostic message envelope |
| `Channel` | `helixon` | Transport interface: `Serve(ctx, handler)` + `Shutdown(ctx)` |
| `Agent` | `agent` | Tool-augmented conversation loop |
| `SessionStore` | `agent` | SQLite session persistence with FTS5 |
| `Registry` | `tooldispatch` | Thread-safe tool registration and dispatch |
| `TracedExecutor` | `tooldispatch` | NDJSON agentrace decorator |
| `Handler` | `fleet` | Concurrent fleet task handler with retry |
| `HybridSearcher` | `memory` | Engram vector + FTS5 blended search |
| `Provider` | `llm` | LLM provider interface (Complete, Stream) |
| `Server` | `platform` | HTTP/SSE platform server |

## Dependencies

- `cloudwego/eino` v0.8.13 -- LLM orchestration framework
- `modernc.org/sqlite` -- Pure Go SQLite (no CGO)
- `spf13/cobra` -- CLI framework
- `stretchr/testify` -- Test assertions
- `prometheus/client_golang` -- Metrics
- `go.opentelemetry.io/otel` -- Tracing

## Identity

- Owner: `nfsarch33` / `jaslian@gmail.com`
- NEVER use work identity for this repo.
