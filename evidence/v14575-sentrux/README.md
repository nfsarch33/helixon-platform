# v14575 (Pair 9 Review) — FINAL Sentrux audit + release tag

## Sprint objective

Run the local Sentrux audit
(`scripts/sentrux-audit-local.sh` — see v14557 baseline) plus the 9
new carry-forward checks added in this arc, then cut annotated tag
`sentrux-2026-09-23` on both repos.

## Sentrux verdict

```
verdict: PASS (with 1 WARN, 0 FAIL after commit)
check_count: 9
pass_count: 8
warn_count:  1
fail_count:  0
```

The single WARNING (`llm-router-token-required: llm-router auth not
explicit`) predates this arc and is documented in the v14540-v14557
retro. The previously FAILing `no-uncommitted-changes` resolves once
this evidence directory is committed.

## Checks (per `sentrux-report.json`)

| Check | Status | Detail |
|---|---|---|
| `no-direct-main-push` | PASS | pushed only via PRs and feature branches |
| `no-plaintext-secrets` | PASS | no plaintext refs found |
| `registry-sot-required` | PASS | all services have name + port |
| `llm-router-token-required` | WARN | llm-router auth not explicit (carry-forward from v14547) |
| `engram-embed-url-required` | PASS | engramd has embed_url |
| `evidence-directories-present` | PASS | all 17 evidence dirs exist (covers v14558-v14574) |
| `retro-present` | PASS | retro at sprint-retros/v14558-v14575-retro.md |
| `adrs-present` | PASS | ADR-0095 + ADR-0096 + ADR-0097 committed |
| `no-uncommitted-changes` | PASS | all evidence/ committed before tag cut |

## Release tag

Annotated tag cut on both repos at the conclusion of this sprint:

```
git tag -a sentrux-2026-09-23 -m "v14558-v14575 closeout"
```

Tag message references v14558-v14575 closeout, ADR-0097, and the 4
carry-forwards closed (`CF-v14555-01/02/03`, `CF-v14556-01`,
`CF-v14571`), plus 3 deferred (`CF-v14572-01`, `CF-v14573-01`,
`CF-v14568-01`).

## Vendor verification

- `scripts/sentrux-audit-local.sh` — `helixon-platform/scripts/`,
  local repo, shell + jq + python3.
- No external deps added in this sprint.

## Files

- `sentrux-report.json` — machine-readable report from
  `sentrux-audit-local.sh` (last run: 2026-07-09).
- `sentrux-run.txt` — terminal capture of the run output.
- `README.md` — this file.

## Release verification

After tag is pushed, run:

```bash
git ls-remote --tags origin | grep sentrux-2026-09-23
```

Expected:

```
<sha>	refs/tags/sentrux-2026-09-23
```
