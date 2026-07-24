# v18736-3 — cross-repo leak prevention parity — EXEC REPORT

- **Sprint range:** v18726–v18739 overnight
- **Story:** v18736-3 — cross-repo leak prevention parity
- **Worktrees:**
  - `~/runs/worktrees/helixon-platform-v18736-3` (branch `feat/v18736-3-leak-prevention-parity`)
  - `~/runs/worktrees/helix-dev-tools-v18736-3` (branch `feat/v18736-3-pre-commit-leak-hook`)
- **Branches pushed:**
  - `nfsarch33/helixon-platform:feat/v18736-3-leak-prevention-parity` → commit `3d2722a`
  - `nfsarch33/helix-dev-tools:feat/v18736-3-pre-commit-leak-hook` → commit `630584c`
- **PRs opened:**
  - https://github.com/nfsarch33/helixon-platform/pull/62
  - https://github.com/nfsarch33/helix-dev-tools/pull/69
- **Completed at:** 2026-07-24T22:39+10:00

## Outcome

| Sub-task | Status | Evidence |
|---|---|---|
| `.a` Baseline scan | ✓ | 49 internal_ip + 1 ssh_key_path + 1 op_read_pipe (initial) |
| `.b` op-read-pipe fix | ✓ | `reports/research/v18692-1-live-llm-e2e.md` (13+/2-) |
| `.c` evidence/handoff annotations | ✓ | 10 evidence + 3 handoff files (allow-file internal_ip/ssh_key_path) |
| `.d` CI/K8s/scripts annotations | ✓ | 5 files: ci/sentrux.gitlab-ci.yml, k8s/argocd/apps.yaml, observability/prometheus.yml, scripts/k3s-agent-join*.sh |
| `.e` Re-scan zero findings | ✓ | `runx shell-leak-scan --repo helixon-platform` → 0 blocking |
| `.f` helix-dev-tools hook | ✓ | `internal/cli/githook_public_repo_leak_prevention.go` (+510 LOC) |
| `.g` Hook unit + e2e tests | ✓ | 11 new tests; `go test -count=1 ./internal/cli/...` GREEN |
| `.h` Build helix-dev-tools | ✓ | `~/bin/helix-dev-tools` rebuilt at 2026-07-24T22:39+10:00 (26 MB) |
| `.i` helixon-platform commit + PR | ✓ | commit `3d2722a`; PR #62 |
| `.j` helix-dev-tools commit + PR | ✓ | commit `630584c`; PR #69 |

## Files Changed

### `nfsarch33/helixon-platform` (PR #62)

```
 ci/sentrux.gitlab-ci.yml                                 |  1 +
 evidence/v14545-argocd/README.md                         |  1 +
 evidence/v14554-k3s-join-ci/README.md                    |  1 +
 evidence/v14555-mesh-dashboards/README.md                |  1 +
 evidence/v14558-mesh-ping/README.md                      |  1 +
 evidence/v14559-win4-troubleshooting/README.md           |  1 +
 evidence/v14561-browser-ui/README.md                     |  1 +
 evidence/v14563-wsl3-join/README.md                      |  1 +
 evidence/v14563-wsl3-join/cluster-diagram.md             |  1 +
 evidence/v14564-argocd-resync/README.md                  |  1 +
 evidence/v14571-fleet-agent-live/fleet-agent-register.sh |  1 +
 k8s/argocd/apps.yaml                                     |  1 +
 observability/prometheus.yml                             |  1 +
 reports/research/v18692-1-live-llm-e2e.md                | 13 +++++++++++--
 scripts/k3s-agent-join-wsl3.sh                           |  1 +
 scripts/k3s-agent-join.sh                                |  1 +
 session-handoffs/v14541-handoff.md                       |  1 +
 session-handoffs/v14542-handoff.md                       |  1 +
 session-handoffs/v14543-handoff.md                       |  1 +
 19 files changed, 29 insertions(+), 2 deletions(-)
```

### `nfsarch33/helix-dev-tools` (PR #69)

```
 internal/cli/githook.go                                          |   1 +
 internal/cli/githook_pre_commit.go                               |   6 +
 internal/cli/githook_public_repo_leak_prevention.go              | 510 +++++++++++
 internal/cli/githook_public_repo_leak_prevention_test.go         | 449 +++++++++++
 4 files changed, 966 insertions(+), 2 deletions(-)
```

## Verification

```bash
$ runx shell-leak-scan --repo helixon-platform
# 0 blocking findings (baseline was 49 internal_ip + 1 ssh_key_path + 1 op_read_pipe)

$ go test -count=1 ./internal/cli/...
ok  	github.com/nfsarch33/helix-dev-tools/internal/cli	10.186s

$ ~/bin/helix-dev-tools githook public-repo-leak-prevention --help
pre-commit: block shell-leak findings (Tailscale IPs, ssh_key_path,
op_read_pipe, env_leak, tmp_script) on staged files

$ cd ~/runs/worktrees/helixon-platform-v18736-3 && \
  ~/bin/helix-dev-tools githook public-repo-leak-prevention
$ echo $?
0   # clean exit — annotations honoured, zero findings
```

## Annotation Strategy

The 18 annotated files fall into 3 categories:

1. **Operator-relevant evidence files** (`evidence/v1454*-v1457*`) — IP literals
   document the actual fleet topology at the time of the original sprint;
   the information is intentionally visible to anyone working on the
   repo. Annotating with `runx-leak-scan: allow-file internal_ip` keeps
   the documentation readable while flagging the IP as intentional.
2. **CI/K8s/scripts** — production routing/registry data that needs the
   real Tailscale IPs to function. Annotation prevents the scanner from
   flagging runtime config as a leak.
3. **Handoff files** (`session-handoffs/v1454*-handoff.md`) — historical
   handoffs that reference live topology.

The `op_read_pipe` finding in `reports/research/v18692-1-live-llm-e2e.md`
was a true vulnerability: the file documented the
`ALIYUN_QWEN_TOKEN_PLAN_KEY=$(op read op://HelixonSafe/.../password)`
pattern, which would print the secret on the argv if copy-pasted.
Replaced with the file-based `op read --out-file -f` pattern that
1Password-usage.mdc recommends and aliased the 1Password item UUID inline.

## New Hook (helix-dev-tools)

The `helix-dev-tools githook public-repo-leak-prevention` command
provides fast first-line defence on personal clones. It:

- Scans staged files via `git diff --cached --name-only`.
- For each scannable file (.md, .sh, .yml, .yaml, .go, .py, .json,
  .tf, .toml), runs the inlined regex catalogue across 5 categories:
  `internal_ip`, `ssh_key_path`, `op_read_pipe`, `env_leak`,
  `tmp_script`.
- Honours both `runx-leak-scan: allow-file <cats>` (file header, first
  30 lines) and per-line `runx-leak-scan: allow` directives so
  `runx shell-leak-annotate` output is the single source of truth.
- Exits 1 with structured stderr listing each blocking finding +
  remediation hint if any blocking category is detected.
- Exits 0 with one-line note if either opt-out is active:
  - `git config hooks.allowPublicRepoLeak true` (per-repo)
  - `HELIX_DEV_ALLOW_PUBLIC_REPO_LEAK=1` (one-shot env var)

### Why duplicate the regex catalogue?

`helix-dev-tools` and `runx` are separate Go module roots with
different release cadences. Importing `runx/internal/shellleak` would
couple helix-dev-tools to a moving target. Inlining keeps the hook
self-contained, deterministic, and decoupled. The catalogue is small
(5 categories, ~10 regexes) so duplication cost is low.

## Carry-forward

- **None** for v18736-3 itself.
- Future v18740+ stories can wire the new hook into `~/.config/git/hooks/pre-commit`
  on personal clones via a one-line `make cursor-hooks` or
  `runx doctor hooks` integration; not in scope for this sprint.

## Refs

- v18734-2 evidence: `evidence/v18734-2-shell-leak-zero-finding-2026-07-24.md`
- `1password-usage.mdc` — file-based `op read` pattern.
- `no-shell-leak.mdc` Cat 4 — argv-leak ban.
- Sprint plan: `~/.cursor/plans/v18726-v18739_overnight_carried-forward_and_helixchannel_pilot_e2e_and_llm_cluster_router_encryption_b10f2fd2.plan.md`