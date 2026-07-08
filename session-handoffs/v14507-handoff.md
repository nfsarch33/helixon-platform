# v14507 Handoff - Pair 2 Review: CI Gauntlet, devcontainer, Taskfile

- **Sprint**: v14507 (Pair 2 Review/Self-improve)
- **Closed**: 2026-07-08 (UTC+10)
- **Status**: COMPLETE - all CI config files delivered. End-to-end CI runs deferred to operator (`task install` then `task ci`).

## Deliverables (PR #8)
1. **.golangci.yml** (strict lint, 23 linters)
2. **Taskfile.yml** (idempotent task runner, `task ci` runs full gauntlet)
3. **.trivyignore** (vulnerability false-positive placeholder)
4. **.nancy-ignore** (Go module CVE ignore placeholder)
5. **.devcontainer/devcontainer.json** (Go 1.26, helm, docker-in-docker)
6. **.gitlab-ci.yml** (added trivy-fs, nancy, helm-lint as required checks)

## Required-checks gate (after PR merges)
- vet
- lint (golangci-lint, allow_failure=false)
- test
- build
- govulncheck
- gosec (allow_failure=false, was true)
- trivy-fs (NEW, HIGH/CRITICAL)
- nancy (NEW)
- helm-lint (NEW)

## Verification this session
- `helm lint charts/helixon-control-plane` → 0 failed
- `git diff` reviewed: no breaking changes to existing CI

## Operator actions required
1. Run `task install` to populate golangci-lint, govulncheck, gosec, staticcheck into $GOPATH/bin
2. Verify pipeline runs on next MR — all required checks should gate merge

## Carry-forward to v14508
1. Add agentrace SDK to internal/helixon/controlplane/
2. Add OTLP/HTTP exporter to /healthz and /readyz
3. Add Temporal workflow `sprint_lifecycle` (5 activities)
4. Add idempotency + retry policy helper
5. Add cost-observability stub

## Risk register additions
- golangci-lint v1.x may take 5+ minutes on full repo; CI timeout set to 5m per plan
- trivy + nancy require network egress to trivy-db + sonatype; ensure CI runner has outbound
- Devcontainer uses mcr.microsoft.com base; depends on Docker socket availability
