# Sprint v17808 Retro — 7-Gate QA Battery

**Sprint:** v17808 — Comprehensive 7-gate QA battery
**Date:** 2026-07-11T18:55+10:00
**Author:** cursor-parent@wsl3

## What shipped

- 7-gate QA battery documented + executed GREEN
- 1 commit: `a9ce04d` gofmt 5 stale files
- KPI report: `reports/research/sprint-v17808-hygiene-kpi.md`
- Capsule: `global-memories/capsules/v17808-7gate-qa.md`
- Sentrux baseline saved at `a9ce04d`

## Why a QA-only sprint

The v17807 closeout surfaced gofmt drift on 5 files that hadn't been
caught by any prior sprint's gate battery. This sprint (v17808) is the
turning-point sprint that establishes the canonical 7-gate battery
(per the universal scaffold) so future sprints inherit the gate, not
re-discover it.

## What worked

- Running the gates in a single backgrounded `go test -race -coverprofile`
  with a 600s timeout kept the session wall-clock bounded.
- Sentrux baseline save + gate sequence worked end-to-end on first try.
- `runx shell-leak-scan` already wired into the workspace — 0 findings
  on first scan.

## What was missed

- 5 files had gofmt drift; the prior sprint's gate did not include
  gofmt as a hard gate. Adding it now prevents drift accumulation.
- No existing race gate was wired; only `go test ./...` was used prior.
  The new gate combines `-race -short -coverprofile` for full coverage
  + race detection in one pass.

## Lessons learned

1. **Gate battery is the first-class artifact**, not an afterthought.
   Treating it as story 1-of-7 each sprint prevents drift.
2. **Sentrux baseline must be saved once per worktree**; running `gate`
   without `--save` fails with a "no baseline" error which is correct
   behaviour but needs the user to know to save first.

## Carry-forward

- v17809: Helixon Agent eval-rubric coverage
- v17810: Range closeout

## Compliance

- All 7 gates: GREEN
- 5-file gofmt cleanup: shipped
- Sentrux baseline: saved
- Branch: `feat/v17808-7gate-qa-battery` pushed (1 commit)
- KPI + capsule + retro + daily-startup entry: produced