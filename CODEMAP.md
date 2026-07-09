# CODEMAP.md — helixon-platform

> **Generated**: 2026-07-09T11:10+10:00
> **Owner/Machine-Id**: cursor-parent@win3-wsl3
> **Source**: `go list ./...` (v17004-4)
> **Ref**: Loop Engineering R5/R6 (v16713, v16869)

This document maps every package in `helixon-platform` to its role and dependencies. It is the canonical entry point for any agent or operator onboarding into the runtime.

---

## Top-level layout

```
helixon-platform/
├── cmd/             # Operator-runnable binaries (cobra)
├── internal/        # Library code; importable only inside this module
├── tools/           # Internal tooling binaries (cobra; not deployed)
├── charts/          # Helm charts for k3s/k8s deploy
├── docs/            # Architecture / operator docs
├── bin/             # Pre-built binaries (gitignored)
└── CODEMAP.md       # (this file)
```

---

## `cmd/` — operator binaries

| Package | Role | Deploy target |
|---|---|---|
| `cmd/helixon` | Main runtime entrypoint; agent loop + tool dispatch | k3s `helixon` ns |
| `cmd/helixon-eval` | Eval harness runner; 14-dim rubric | local + k3s `cicd` ns |
| `cmd/github-sync` | GitHub ↔ GitLab sync worker (mirrors `cursor-global-kb`) | k3s `cicd` ns |
| `cmd/send-end-email` | Session-end email notifier (vendor rotation + notifydb) | cron / launchd |
| `cmd/svcregistryd` | Service registry daemon; port collision detection | k3s `devops` ns |

---

## `internal/` — library packages

### Cross-cutting primitives

| Package | Role | Depends on |
|---|---|---|
| `internal/llm` | OpenAI-compatible provider abstraction (MiniMax, Aliyun, OpenAI); request/response types | (none) |
| `internal/toolresult` | Standardised tool-execution result struct (status, output, error, latency, cost, idempotency-key, hash) | `internal/llm` (transitively) |
| `internal/loopguard` | Sliding-window hash detector + circuit-breaker for runaway tool-call loops (v17003 MVP-1) | (none) |
| `internal/notify` | Email dispatcher; weighted least-used vendor rotation (Resend 2x, Brevo 1x, SMTP2GO off); idempotency keys; HTML templates | `internal/toolresult` |
| `internal/notifydb` | SQLite persistence for email audit trail; query-by-plan | (none) |

### Helixon agent core

| Package | Role | Depends on |
|---|---|---|
| `internal/helixon` | `Runtime` composition root; orchestrates agent loop, channels, tools, memory | `internal/helixon/agent`, `internal/helixon/channel`, `internal/helixon/tooldispatch`, `internal/helixon/memory`, `internal/helixon/controlplane` |
| `internal/helixon/agent` | `ToolExecutor` interface; agent session/turn logic | `internal/llm` |
| `internal/helixon/builtins` | Built-in tool implementations (read_file, write_file, etc.) | `internal/helixon/tooldispatch` |
| `internal/helixon/channel` | I/O channels (stdio, MCP-stdio) | (none) |
| `internal/helixon/tooldispatch` | Tool registry + dispatch; `TracedExecutor` + `LoopGuardExecutor` wrappers | `internal/loopguard`, `internal/toolresult`, `internal/llm` |
| `internal/helixon/memory` | Hybrid search (BM25 + vector); Engram client | `internal/llm` |
| `internal/helixon/controlplane` | SprintBoard client; `/healthz` + `/readyz` HTTP handlers | (none) |
| `internal/helixon/platform` | Platform-level abstractions (capability registry) | (none) |
| `internal/helixon/safety` | Tiered destructive-op guard (CRITICAL/HIGH); Agentrace hook | (none) |
| `internal/helixon/fleet` | Fleet doctor probes; cross-node coordination | `internal/helixon/safety` |
| `internal/helixon/dashboard` | Static metrics dashboard | (none) |

### Eval + chaos

| Package | Role | Depends on |
|---|---|---|
| `internal/helixon-eval` | 14-dim rubric engine; eval task harness | `internal/llm` |
| `internal/evalfw` | Lower-level eval framework primitives | (none) |
| `internal/chaos` | Chaos engineering probes (intermittent 4xx, latency) | `internal/helixon/safety` |

### Callbacks + service registry

| Package | Role | Depends on |
|---|---|---|
| `internal/callbacks` | Tool-result callback dispatch (used by retry policy + circuit-breaker MVP) | `internal/toolresult` |
| `internal/svcregistry` | Service registry client (port collision detection) | (none) |

---

## `tools/` — internal-only binaries (not deployed)

| Package | Role |
|---|---|
| `tools/email-vendor-rotation` | Weighted LRU email vendor audit log tool |
| `tools/session-end-email-verify` | Smoke test for session-end email flow |

---

## `charts/` — k3s/k8s

| Chart | Role |
|---|---|
| `charts/helixon-platform` | Helm chart for `cmd/helixon` deployment (PDB, HPA, ServiceMonitor, ingress) |

---

## `docs/` — operator-facing

| File | Role |
|---|---|
| `docs/architecture.md` | High-level architecture diagram + data flow |
| `docs/operator-runbook.md` | Day-2 operations (rotate keys, drain nodes, etc.) |

---

## Cross-package dependency graph (high level)

```
cmd/helixon ──> internal/helixon
                 │
                 ├─> internal/helixon/agent
                 ├─> internal/helixon/channel
                 ├─> internal/helixon/tooldispatch ──> internal/loopguard
                 │                                  ──> internal/toolresult
                 ├─> internal/helixon/memory ──> internal/llm
                 ├─> internal/helixon/controlplane
                 ├─> internal/helixon/safety
                 └─> internal/helixon/fleet ──> internal/helixon/safety

cmd/send-end-email ──> internal/notify ──> internal/toolresult
                                        ──> internal/notifydb

cmd/svcregistryd ──> internal/svcregistry

cmd/helixon-eval ──> internal/helixon-eval ──> internal/evalfw

cmd/github-sync ──> (no internal deps; talks to GitHub + GitLab APIs)
```

---

## Test conventions

- Every package has `*_test.go` next to its implementation.
- TDD-first: RED test → GREEN impl → coverage lift.
- Coverage floor: 70% per package (`helixon-platform/internal/*`).
- `go test -race -short` is the default gate.
- `golangci-lint run` + `govulncheck ./...` are run as part of CI gauntlet (Podman, not Docker).

---

## Reference

- Loop Engineering R5 validation: `reports/research/v16713-r5-design.md`
- Loop Engineering R6 validation: `reports/research/v16869-*-validation.md`
- 6 Helixon agent MVPs: `session-handoffs/capsules/v17003-loopguard-mvp-1.md` + 5 more to ship in v17001-v17200 range.
- ToolResult struct: `internal/toolresult/result.go` (v17004-2).
- LoopGuard: `internal/loopguard/` (v17003-2).

## Machine-Id

`cursor-parent@win3-wsl3` (created 2026-07-09T11:10+10:00; v17004-4).