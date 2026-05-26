# Helixon Platform -- Agent Guidelines

- Repo: `https://github.com/nfsarch33/helixon-platform`
- Purpose: Standalone Go agent runtime extracted from the business stack
  monorepo. Provides the core Helixon lifecycle, tool dispatch, channels,
  memory, and platform server.

## Build & Test

```bash
go build ./...
go test -race -cover ./...
go vet ./...
```

## Coding Conventions

- Go, strict typing. `go vet` and `golangci-lint run` clean.
- No secrets in committed files.
- Conventional commits: `type(scope): message`.
- TDD: write failing test first, then implementation.

## Project Structure

```
cmd/helixon/       CLI entry point (serve, doctor, repl, platform, eval, task)
internal/helixon/  Core packages (agent, builtins, channel, controlplane,
                   dashboard, memory, platform, safety, tooldispatch)
internal/evalfw/   Evaluation framework
```

## Key Types

- `helixon.Runtime` -- the agent lifecycle manager
- `helixon.RuntimeConfig` -- YAML-driven configuration
- `helixon.IncomingMessage` / response -- channel I/O
- `helixon.Provider` -- LLM provider interface
- `tooldispatch.Registry` -- tool registration and dispatch
- `platform.Server` -- HTTP/SSE server for API access
- `evalfw.Suite` / `evalfw.Runner` -- evaluation harness

## Identity

- Personal repos: `nfsarch33` / SSH key for GitHub
- NEVER use work identity for this repo.
