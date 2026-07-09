# CODEMAP.md — `internal/helixon/agent` (Helixon Agent)

> **Generated**: 2026-07-09T11:11+10:00
> **Owner/Machine-Id**: cursor-parent@win3-wsl3
> **Ref**: Loop Engineering R5/R6 (v16713, v16869); Sprint v17004-5 in range v17001-v17200

This document maps the Helixon Agent module — the tool-augmented conversation loop that turns LLM responses into actual tool calls and aggregates results.

---

## Package: `internal/helixon/agent`

The agent module implements the core "agent loop":

```
for iteration in 1..MaxIterations:
  response := provider.Chat(messages, tools)
  if response.HasToolCall():
    result := tools.Execute(toolCall.Name, toolCall.Args)
    messages.append(toolResultMessage(result))
    continue
  else:
    return response.Content  // final answer
```

### Files

| File | Role |
|---|---|
| `agent.go` | `Agent` struct + `Run()` method (the loop above); `RunResult`; `ToolExecutor` interface |
| `session.go` | `SessionStore` (SQLite-backed) — message history persistence + resume |
| `agent_test.go` | TDD tests for `Agent.Run()` |
| `session_test.go` | TDD tests for `SessionStore` |

### Key types

| Type | Role |
|---|---|
| `Agent` | The agent loop orchestrator |
| `Config` | Tunable behaviour (max iterations, max tokens, timeout, system prompt) |
| `ToolExecutor` | Interface that `*tooldispatch.Registry` satisfies; pluggable for testing |
| `SessionStore` | Persists message history + run metadata (SQLite at `helixon-sessions.db`) |
| `RunResult` | Final return value: session ID, final content, iteration/token counts, error |

### Defaults (from `Config.withDefaults()`)

| Field | Default |
|---|---|
| `MaxIterations` | 25 |
| `MaxTokens` | 128000 |
| `Timeout` | 5 minutes |
| `SystemPrompt` | (empty — caller-provided) |
| `Logger` | `slog.Default()` |

### Error sentinels

| Error | Cause |
|---|---|
| `ErrMaxIterations` | Hit `MaxIterations` without producing final answer |
| `ErrBudgetExhaust` | Token budget depleted |
| `ErrTimeout` | Overall run timeout exceeded |

### Dependencies

| Package | Role |
|---|---|
| `internal/llm` | Provider abstraction (MiniMax, Aliyun, OpenAI) — `provider.Chat(messages, tools)` |
| `internal/callbacks` | Tool-result callback dispatch (used by retry policy + circuit-breaker MVP) |

---

## Loop Engineering R5/R6 retrofit touchpoints

Per `reports/research/v16713-r5-design.md` and `reports/research/v16869-*-validation.md`, the following 6 MVPs touch this package:

| MVP | Touchpoint |
|---|---|
| LoopGuard | `agent.go` calls `tools.Execute`; LoopGuardExecutor (from v17003) wraps the call |
| ToolResult | `agent.go` `toolResult, toolErr := a.tools.Execute(...)` — replace with `ExecuteToolResult` returning `toolresult.ToolResult` (v17004-3) |
| CODEMAP.md | This file (v17004-5) |
| MiniMax circuit-breaker | `provider.go` (in `internal/llm`) wraps provider calls |
| Context persistence | `session.go` `SessionStore` extended to persist via Engram (v17006) |
| Checkpoint cadence | `agent.go` emits Agentrace checkpoint events every N iterations (v17007) |

---

## Test coverage

Current coverage (2026-07-09T11:11+10:00): see `go test -cover ./internal/helixon/agent/...`.

---

## Reference

- Plan: range v17001-v17200, Sprint 4 (v17004-5).
- Companion: `CODEMAP.md` (repo root, v17004-4).
- LoopGuard: `internal/loopguard/` (v17003).
- ToolResult: `internal/toolresult/` (v17004).

## Machine-Id

`cursor-parent@win3-wsl3`