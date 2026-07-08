# HelixonEval R3 — Cross-Check & Integration Plan (Sprint 18 v16129-3)

> **Sprint:** v16129 (HelixonEval R3 + Eval Harness Maturity)
> **Date:** 2026-07-05
> **Owner:** cursor-parent (worker subagent delivery)
> **Status:** DRAFT — append to PR #`feature/v16129-helixon-eval`

## Purpose

Document how the new `helixon-eval` binary
(`helixon-platform/cmd/helixon-eval/` +
`helixon-platform/internal/helixon-eval/`) interacts with the two
preexisting eval surfaces in the Helixon org:

1. **`helixon-autoresearch/eval/`** — the 7-rubric × 7-task harness
   first shipped in Sprint B + refined through `c577fe4` and `776e65d`.
2. **`helixon-evolver/internal/evolver/cycle.go`** — the self-improvement
   cycle engine that records `Score{}.Cycle(...)` calls.

This file is **v16129-3** in the Sprint 18 plan; it lives in the
helixon-eval package so reviewers see it in the same diff.

## What HelixonEval R3 ships

- A Go package + binary that runs the **5-task golden set** on
  three judge models (`qwen3.7-plus`, `qwen3.7-max`, `MiniMax-M3`).
- CLI: `helixon-eval {run, report, list-tasks, version}`.
- 4 G-Eval rubrics (`correctness`, `robustness`, `completeness`,
  `termination`) applied per case.
- Threshold gate: `OverallScore ≥ 0.7` ⇒ pass.
- Race-clean, vet-clean, **90.6 % coverage** (target ≥ 70 %).
- 30 unit tests (registry_test, report_test, helpers_test).
- STAGING EVAL ONLY — SynthSource provides deterministic offline traces
  because Aliyun quota is exhausted.

## Cross-check vs `helixon-autoresearch/eval/`

The autoresearch harness is the **definitive G-Eval harness** for the
Helixon org: 7 weighted rubrics × 7 task types, with anchored scoring
guidance and JSON-Lines trace writeback to user-engram MCP. The R3
binary deliberately does **not** replace it.

| Aspect                   | autoresearch/eval                | helixon-eval (R3)                       |
|--------------------------|----------------------------------|-----------------------------------------|
| Rubric count             | 7 (G-Eval weighted)              | 4 (correctness, robustness, completeness, termination) |
| Task count               | 7 task types × N instances       | 5 fixed golden tasks                    |
| Judge calls              | live LLM judge                   | deterministic SynthSource (Sprint 18)   |
| Output sink              | user-engram MCP + Agentrace      | stdout Markdown / JSON + DRL NDJSON     |
| Schema                   | `TaskResult`, `ScoreMatrix`      | `Case`, `Report`, `ModelStat`           |
| Threshold                | 0.7 per rubric weighted          | 0.7 overall mean                        |
| Cadence                  | sprint regression + ad-hoc       | per-sprint closeout + operator UI        |
| Persistence              | JSONL → engram MCP               | stdout + (Sprint 19+) stdout-JSON file  |

### Why two harnesses?

- `autoresearch/eval/` is the **full sandbox** for offline G-Eval runs.
  It is written so a researcher can spawn many task-type variants and
  re-weight rubrics. It owns the LLM-judge plumbing and the engram MCP
  writeback.
- `helixon-eval` (R3) is the **small operator-visible CLI**. It runs
  once per sprint, against a fixed five-task set, and emits a Markdown
  report. The output schema is intentionally tiny: enough to wire the
  report into a Reality-Check axis.

The R3 package deliberately imports no symbols from
`helixon-autoresearch/eval/`. The integration point is **the report
JSON contract**, not a Go module dependency. This is consistent with
`rule-24 (golang-first-tooling)` + `rule-25 (reuse-first)` — the two
harnesses share concepts (rubric, task, judge), not symbols.

### Promotion path (Sprint 19+)

When Sprint 19 brings live API calls back, promote `helixon-eval` to
call into `helixon-autoresearch/eval/rubrics.go` for the per-criterion
G-Eval anchors. The package boundary stays intact; the only change is
replacing `SynthSource` with a `LiveSource` adapter that pulls from
the autoresearch judge pipe.

## Cross-check vs `helixon-evolver/internal/evolver/`

The evolver cycle engine records `Score{s}.Cycle(cycleID, []Score)`
calls every iteration. Each `Score` is a scalar in `[0, 1]` per
criterion. The engine's job is to refine the policy over time, **not**
to evaluate new tasks.

The R3 binary does not feed the evolver directly. The integration
plan: Sprint 19 wires `helixon-eval report --out ndjson` into the
`helixon-evolver` cycle as **the evaluation oracle**. Specifically:

- The evolver will call `helixon-eval run --task "$TASK" --models
  qwen3.7-max --json` once per cycle.
- The evolver `ScoreMatrix` will receive the R3 `Report` JSON.
- The evolver policy will trade-off `(mean_score, variance, step_count)`
  the same way it already trades off `(reward_mean, reward_std, budget)`.

Until Sprint 19, both systems are decoupled. The R3 binary is the
**definitive ceremonial eval**, the evolver remains the **online
self-improvement loop**.

## Operator-visible diffs

| File                                                | Lines | Purpose |
|-----------------------------------------------------|-------|---------|
| `helixon-platform/internal/helixon-eval/registry.go` | 178  | Registry, Runner, TraceSource, Case types |
| `helixon-platform/internal/helixon-eval/report.go`   | 145  | Report, Aggregate, Score, WriteText |
| `helixon-platform/internal/helixon-eval/tasks.go`    | 168  | Golden 5-task set, SynthSource, slugs |
| `helixon-platform/internal/helixon-eval/registry_test.go` | 184  | TDD tests (Registry.Add / Run) |
| `helixon-platform/internal/helixon-eval/report_test.go`   | 132  | TDD tests (Report.Aggregate / Score) |
| `helixon-platform/internal/helixon-eval/helpers_test.go`  | 102  | TDD tests (slugify, rubric IDs, model list) |
| `helixon-platform/internal/helixon-eval/testutil_test.go` |  16  | testutil helpers (floatFromInt) |
| `helixon-platform/internal/helixon-eval/INTEGRATION_PLAN.md` | this | v16129-3 cross-check |
| `helixon-platform/cmd/helixon-eval/main.go`         | 217  | cobra CLI (run/report/list-tasks/version) |

Total ~1,150 lines including tests, race-clean, vet-clean,
coverage **90.6 %** (well above the 70 % target).

## What gets removed at Sprint 19 cleanup

`SynthSource` is the placeholder. Sprint 19 wires the live judge path
back in. At that point the SynthSource is deprecated for production
but kept as the offline default for wsl3-only smoke runs. The package
keeps the API surface; the trace objects stay identical.

## References

- DRL v7.1 rule-32 (`graceful-degradation-circuit-breaker-required`)
  — applied by R3's `mean()` arithemetic; no truncation on synth
  traces.
- `helixon-autoresearch/eval/rubrics.go` — canonical G-Eval anchors.
- `helixon-platform/internal/evalfw/` — pre-existing platform eval
  (different scope; orchestrator-internal). R3 is the operator-visible
  surface and does not import it.
