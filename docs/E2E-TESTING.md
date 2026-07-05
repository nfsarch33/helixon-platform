# Helixon E2E Testing Guide

> **Status:** MVP-ready (Sprint v16041 added 2026-07-05).
> **Scope:** How to add, run, and debug end-to-end tests for `helixon-platform`.

This document describes the existing e2e test scaffolding and the
pattern for adding new e2e tests. E2E tests live in
`internal/helixon/*_test.go` and use the `sequentialProvider` mock
LLM to script multi-turn conversations.

## 1. Existing e2e tests

| File | What it tests |
|----|----|
| `agent_e2e_test.go` | Full agent loop: provider → tool dispatch → response |
| `runtime_e2e_v8800_test.go` | Runtime lifecycle (start, run, stop) |
| `validate_v8800_test.go` | Config validation |
| `shutdown_v8800_test.go` | Graceful shutdown |
| `provider_v8800_test.go` | LLM provider integration (mocked) |
| `channel_mcpstdio_v8800_test.go` | MCP stdio channel end-to-end |
| `lifecycle_test.go` | Lifecycle coordinator (init/configure/run/shutdown) |
| `memory_test.go` | Memory L1/L2 read/write integration |

Total: 8 e2e test files covering the full agent surface area.

## 2. The `sequentialProvider` pattern

Most e2e tests use a `sequentialProvider` mock that returns scripted
responses in order. This lets a test drive a multi-turn conversation
without needing a real LLM.

### 2.1 Pattern

```go
type sequentialProvider struct {
    responses []*llm.CompletionResponse
    idx       int
}

func (p *sequentialProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
    if p.idx >= len(p.responses) {
        return nil, fmt.Errorf("no more scripted responses")
    }
    r := p.responses[p.idx]
    p.idx++
    return r, nil
}
```

### 2.2 Example: scripted tool call → response

```go
func TestAgent_ProcessToolCall(t *testing.T) {
    provider := &sequentialProvider{
        responses: []*llm.CompletionResponse{
            // Turn 1: provider returns a tool call
            {
                Choices: []llm.Choice{{
                    Message: llm.Message{
                        Role: "assistant",
                        ToolCalls: []llm.ToolCall{{
                            ID:   "call_1",
                            Type: "function",
                            Function: llm.FunctionCall{
                                Name:      "echo",
                                Arguments: `{"text":"hello"}`,
                            },
                        }},
                    },
                }},
            },
            // Turn 2: provider returns final text after seeing tool result
            {
                Choices: []llm.Choice{{
                    Message: llm.Message{
                        Role:    "assistant",
                        Content: "The tool returned: hello",
                    },
                }},
            },
        },
    }

    // ... build agent with provider, run, assert final content
}
```

### 2.3 Why this pattern

- **Deterministic** — no flakiness from real LLM responses
- **Fast** — no network calls
- **Offline** — runs in CI without API keys
- **Scriptable** — any conversation shape can be modeled

## 3. Running e2e tests

### 3.1 Local

```bash
cd /home/jason/helixon-platform
go test -race -run TestAgent ./internal/helixon/...
```

### 3.2 Full suite

```bash
go test -race ./...
```

CI runs this on every push (`.gitlab-ci.yml` `test` stage).

### 3.3 Skip e2e in short mode

```bash
go test -short ./...  # skips tests that take >100ms
```

Add `testing.Short()` guard at the top of long-running e2e tests:

```go
if testing.Short() {
    t.Skip("skipping e2e in short mode")
}
```

## 4. Adding a new e2e test

### 4.1 Naming

- File: `internal/helixon/<feature>_e2e_test.go`
- Test function: `Test<Feature>_<Scenario>` (PascalCase, snake_case split)

### 4.2 Template

```go
package helixon_test

import (
    "context"
    "testing"
    "time"

    "github.com/nfsarch33/helixon-platform/internal/helixon/agent"
    "github.com/nfsarch33/helixon-platform/internal/llm"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestNewFeature_HappyPath(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping e2e in short mode")
    }

    // 1. Set up mock provider
    provider := &sequentialProvider{
        responses: []*llm.CompletionResponse{
            // ... scripted responses
        },
    }

    // 2. Build agent
    a, err := agent.New(agent.Config{
        Provider: provider,
        // ... other config
    })
    require.NoError(t, err)

    // 3. Run
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    resp, err := a.Run(ctx, "test input")
    require.NoError(t, err)

    // 4. Assert
    assert.Contains(t, resp.Content, "expected substring")
    assert.Equal(t, 1, provider.idx, "provider should have been called exactly once")
}
```

### 4.3 Coverage expectations

New features should add at minimum:

- 1 happy-path e2e test
- 1 error-path e2e test (tool failure, provider timeout, etc.)
- 1 unit test for pure logic

## 5. Debugging e2e failures

### 5.1 Single test verbose

```bash
go test -race -v -run TestAgent_ProcessToolCall ./internal/helixon/...
```

### 5.2 Race detector

All e2e tests run with `-race`. If a race is detected, the test fails
with a `WARNING: DATA RACE` header and a stack trace. The stack trace
points to two goroutines accessing the same memory without
synchronization. Fix by adding a mutex or channel.

### 5.3 Timeout

E2E tests have a 30s default timeout via `context.WithTimeout`. If a
test times out, the goroutine dump is written to stderr. Common causes:

- Mock provider ran out of scripted responses (increase buffer)
- Tool dispatch hung (check tool timeout config)
- Channel send/receive mismatch (check channel close)

### 5.4 Common bugs

| Symptom | Likely cause |
|----|----|
| `no more scripted responses` | Provider called more times than scripted; add more responses |
| `context deadline exceeded` | Test slower than 30s; bump timeout |
| Race detector hits | Concurrent access to `provider.idx`; add mutex (use `atomic.Int` instead) |
| `tool X not found` | Tool not registered in agent config |

## 6. References

- `internal/helixon/agent_e2e_test.go` — canonical example
- `internal/llm/` — provider interfaces
- `internal/helixon/tooldispatch/` — tool dispatch tests
- [RUNBOOK.md](./RUNBOOK.md) — production ops
- [DEPLOYMENT.md](./DEPLOYMENT.md) — deploy + upgrade