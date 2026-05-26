# Helixon Platform

Helixon is a Go agent runtime for building autonomous AI agents with
tool dispatch, multi-channel input, memory integration, and lifecycle
management.

## Features

- **Agent runtime** with configurable LLM providers (OpenAI-compatible,
  vLLM, local models)
- **Tool dispatch** with built-in tools (shell, file read/write, web
  fetch) and extensible registry
- **Multi-channel input**: REPL, HTTP webhooks, MCP stdio
- **Memory integration**: Engram, workspace, and hybrid memory backends
- **Platform HTTP/SSE server** for browser and API access
- **Control plane**: SprintBoard ticket claiming, heartbeat, A2A client
- **Dashboard**: agent workload, CI/CD status, sprint progress views
- **Safety layer**: cost guards, input validation, output sanitisation
- **Evaluation framework**: suite runner with pass/fail/warn verdicts
  and NDJSON reporting

## Quick start

```bash
go build -o helixon ./cmd/helixon
./helixon doctor
./helixon repl --config helixon.yaml
./helixon serve --config helixon.yaml
./helixon platform --addr 127.0.0.1:8787
```

## Architecture

```
cmd/helixon/          CLI binary (serve, doctor, repl, platform, eval, task)
internal/
  helixon/            Core runtime, config, agent loop, channels, providers
    agent/            Agent and session management
    builtins/         Built-in tool implementations
    channel/          Channel adapters (MCP, REPL, webhook)
    controlplane/     SprintBoard, heartbeat, A2A client
    dashboard/        HTTP dashboard views
    memory/           Engram, Mem0, workspace, hybrid memory
    platform/         HTTP/SSE platform server
    safety/           Cost guards, validation, sanitisation
    tooldispatch/     Tool registry and agentrace tracing
  evalfw/             Evaluation framework (suite runner, reporter)
```

## Configuration

Create a `helixon.yaml`:

```yaml
agent_id: my-agent
session_dsn: "file:helixon.db?cache=shared&mode=rwc"
max_iterations: 25
max_tokens: 4096
timeout: 120s
heartbeat_every: 30s

provider:
  kind: openai
  base_url: http://127.0.0.1:8000/v1
  model: your-model-name
  timeout: 60s
```

## Build

```bash
go build -o helixon ./cmd/helixon
go test -race -cover ./...
go vet ./...
```

## Docker

```bash
docker build -t helixon-platform .
docker run --rm -v $(pwd)/helixon.yaml:/app/helixon.yaml helixon-platform serve --config /app/helixon.yaml
```

## License

MIT
