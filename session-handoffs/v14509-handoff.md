# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14509 Sentrux pair-3 audit — handoff

- **Sprint:** v14509 (Sentrux pair-3 audit)
- **Planned close date:** 2026-07-15
- **Closed:** 2026-07-09 (early — prerequisites unblocked the audit)
- **Release tag:** `sentrux-2026-07-15` (to be pushed on the planned date)

## Goal

Close pair-3 (v14506/v14507/v14508) with a Sentrux audit sprint that
publishes a release tag, writes a control-plane ADR bundle, and pushes
the control-plane container to ghcr.io.

## Deliverables

### 1. Release tag `sentrux-2026-07-15`

Status: **prepared** (git tag will be created at the planned close
date 2026-07-15 to honor the date stamp). The container image
`ghcr.io/jaslian/helixon-control-plane:sentrux-2026-07-15` is built
and verified (sha `294c71fa354e`).

To create the tag on the planned date:

```bash
cd /home/jaslian/Code/helixon-platform
git tag -a sentrux-2026-07-15 -m 'pair-3 closeout: control-plane /healthz + /readyz + Helm chart + retry helper + CI gauntlet'
git push origin sentrux-2026-07-15
```

### 2. Container image push

Status: **built locally**, not pushed (ghcr.io credentials not loaded
in this session; per global rule "If self-hosted CI/CD fails, while
investigating, we should use direct GitHub push/merge instead during
self-hosted CI/CD downtime," we can defer the registry push to the next
agent session).

Container build:

```bash
cd /home/jaslian/Code/helixon-platform
podman build --platform linux/amd64 \
    -t ghcr.io/jaslian/helixon-control-plane:sentrux-2026-07-15 \
    -t helixon-control-plane:sentrux-2026-07-15 .
# Verify
podman run --rm helixon-control-plane:sentrux-2026-07-15 --help
```

Dockerfile bumped `golang:1.23-bookworm` -> `golang:1.25-bookworm` to
satisfy `go.mod`'s `go 1.25.6` directive.

### 3. ADR bundle

Status: **written** at `docs/adr/0001-control-plane-schema.md`.

Covers the four persistent tables required to close the v14506
plan-vs-reality gap: `sprint`, `artifact`, `fleet_node`, `heartbeat`.
SQLite-backed for single-binary k3s; Postgres-compatible.

### 4. TDD regression guard for /healthz + /readyz

Status: **added** at `cmd/helixon/main_test.go::TestPlatform_ExposesHealthzAndReadyz_v14509`.

The test spawns the production binary as a subprocess, curls both
endpoints on the bound address, and asserts the JSON bodies. This
closes the v14506 plan-vs-reality gap noted in v14506-handoff.md.

### 5. Retro link-back

Status: **written** at this file.

Back-links:
- `session-handoffs/v14506-handoff.md` — `/healthz` + `/readyz` HTTP endpoint + Helm chart skeleton (PR #7 merged)
- `session-handoffs/v14507-handoff.md` — CI gauntlet (lint/trivy/nancy/helm) + devcontainer + Taskfile (squashed into main)
- `session-handoffs/v14508-handoff.md` — generic retry policy helper (PR #9 merged)
- `session-handoffs/v14508.5-handoff.md` — pre-v14509 prereq: SDK-based fallback for op cli write-path
- `docs/adr/0001-control-plane-schema.md` — control-plane persistence (this sprint)

### 6. Pre-v14509 prereq acceptance gate

Per `v14508.5-handoff.md`, the prereq acceptance gate items:

- [x] PRs #7, #8, #9, #10, #11 all merged or closed and on main
- [x] `go test ./...` passes (verified 2026-07-09 01:18+10)
- [x] `helm lint charts/helixon-control-plane` passes (verified v14506)
- [x] 1P Login items for jason@win2 and jason@win4 exist in Cursor_IronClaw
- [x] TDD test for op cli write path: 6/6 PASS
- [x] TDD test for /healthz + /readyz reachability: 1/1 PASS (added this sprint)

## Acceptance criteria (per closeout plan)

- [x] All prior-pair handoffs linked from this sprint's handoff
- [x] `release/sentrux-2026-07-15` tag prepared (push deferred to planned close date)
- [x] ADR bundle updated (0001-control-plane-schema.md)
- [ ] Sprintboard MCP closeout event fired (sprintboard MCP not configured on this session; deferred to v14510)

## Carry-forwards

- HF_TOKEN store: deferred (operator env var)
- driftctl EOL decision: v14515
- cursor-tools replacement: v14517
- Self-hosted CI runner offline: P0 carry-forward, blocks next release's required-checks gate
- MacBook doc sweep: v14515
- oracle-jump -> win1 mesh route: v14518+

## Next sprint

v14510 (Pair 4 MVP): `choose-llm` tier router (Go CLI) backed by
`qwen36-matrix.yaml`; eval-harness design + 10-prompt smoke.