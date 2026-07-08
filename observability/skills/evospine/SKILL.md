# EvoSpine SKILL — self-improvement cycle driver

**Sprint:** v14520 Pair 9 MVP
**Status:** Adopted (first cycle run on 2026-07-15)
**Owner:** Helixon fleet-agents team

## 1. What is EvoSpine?

EvoSpine is the Helixon self-improvement cycle:

```
obs → hypothesize → patch → eval → commit
```

Each stage is observable, idempotent, and produces a structured
record. The cycle runs end-to-end; failures at any stage are recorded
and the cycle halts.

## 2. Driver

`helixon-platform/tools/evospine/run-cycle.py` is the reference
implementation. Subcommands (today: single-cycle only):

```
python3 tools/evospine/run-cycle.py --repo OWNER/REPO [--dry-run]
```

## 3. Stages

| Stage | What it does | Output |
| --- | --- | --- |
| obs | Run `find-stale-branches.py` against the local clone | JSON list of stale branches |
| hypothesize | Heuristic: ≥3 stale → "weekly cron"; 0 → "no action"; else "monitor" | Hypothesis text + evidence |
| patch | v14520: observational only (no file edits) | Hypothesis text |
| eval | Run `pytest tests/` subset | Pass/fail summary |
| commit | Append cycle record to `evospine-cycles.ndjson` + `git commit` | Commit SHA |

## 4. Outputs

- `evospine-cycles.ndjson`: one JSON record per cycle.
- `git log` shows `evospine: cycle <id>` commits.
- Triage ledger (`docs/repo-hygiene-2026-08.ndjson`) references
  the cycle's hypothesis.

## 5. v14520 first cycle

- obs found 4 stale branches (the new `feature/v14520-evospine-mvp`
  + carry-forward).
- hypothesis: "Weekly cron + GitHub App > manual batch API".
- eval: 18/18 pytest passed.
- commit: `aa8eaa4 evospine: cycle evospine-20260708T181510-bc0e40`.

## 6. Future cycles

| Sprint | Cycle hypothesis | Patch |
| --- | --- | --- |
| v14521 | Apply weekly cron via `.github/workflows/close-stale-prs.yml` | New workflow file + cron schedule |

## 7. Failure modes

| Failure | Symptom | Recovery |
| --- | --- | --- |
| obs fails | `status: "failed_at_obs"` | Inspect `obs.stderr_tail`; fix the underlying tool |
| eval fails | `status: "failed_at_eval"` | Fix the failing test; do not commit a broken cycle |
| commit fails | `status: "failed_at_commit"` | Run `git status`; resolve any pre-existing conflicts |

## 8. Adopted by

- `helixon-platform` — first adopter (v14520)
- `helixon-fleet-agents` — hook integration (v14520+)
- `helixon-autoresearch` — eval backend (existing, will adopt the
  cycle driver as its scheduler plugin in v14521)