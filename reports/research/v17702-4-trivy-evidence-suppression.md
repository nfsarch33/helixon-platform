---
sprint: v17702-4
title: Trivy false-positive suppression in helixon-platform/evidence/
cf: CF-v17612-10
status: complete
date: 2026-07-10
machine-id: cursor-parent@win3-wsl3
---

# Trivy false-positive suppression in helixon-platform/evidence/

## Problem

`trivy fs .` reported **1 CRITICAL** false positive on `evidence/v14568-slack-webhook/1p-items.json:872`,
flagging the literal string `********************` (1Password export's `additional_information`
redaction placeholder) as an AWS access key via Trivy's `aws-access-key-id` heuristic.

The evidence directory contains 1Password exports accumulated across 35 sprints
(`v14540-...` through `v17702-...`) plus planning artefacts, all under
`evidence/`. None of the content is executable code, so vuln/secret scanning
is not only useless but actively noisy.

## Solution

1. **`trivy.yaml`** (new, repo root): skip the entire `evidence/` tree via `scan.skip-dirs`.
   ```yaml
   scan:
     skip-dirs:
       - evidence/
       - "**/evidence/"
   ```
   Trivy auto-loads `trivy.yaml` from the cwd; no `--config` flag needed.

2. **`.trivyignore`** (updated): added `AVD-AWS-0176` (Trivy's internal ID for the
   AWS-access-key-id secret rule) with expiry 2027-07-10 and a documented reason
   referencing the 1Password export pattern. This is a belt-and-braces guard for
   any future scan path that does not honour `trivy.yaml`.

## Verification

**Before** (run on the whole repo without suppression):
```
Total: 1 (UNKNOWN: 0, LOW: 0, MEDIUM: 0, HIGH: 0, CRITICAL: 1)
CRITICAL: AWS (aws-access-key-id)
────────────────────────────────────────
 evidence/v14568-slack-webhook/1p-items.json:872
 872 [     "additional_information": "********************"
```

**After** (run on the whole repo with `trivy.yaml`):
```
Total: 1 (UNKNOWN: 1, LOW: 0, MEDIUM: 0, HIGH: 0, CRITICAL: 0)
└──────────────────┬────────────────┬──────────┬────────┬───────────────────┬───────────────┬────────────────────────────────────────────────────┐
│ golang.org/x/sys │ CVE-2026-39824 │ UNKNOWN  │ fixed  │ 0.42.0            │ 0.44.0        │ Invoking integer overflow in NewNTUnicodeString in │
│                  │                │          │        │                   │               │ golang.org/x/sys/windows                           │
```
The CRITICAL AWS-key false-positive is gone. The single remaining finding is
`golang.org/x/sys/windows` at severity `UNKNOWN` (a Trivy "fixed in newer
version" advisory that does not apply to non-Windows builds). That will be
addressed by `v17703-4` (Trivy transitive CVE bumps in helix-dev-tools) and
the parallel helixon-platform `go.mod` bump that lands in `v17704-1`.

## Files changed

- `trivy.yaml` — new (9 lines).
- `.trivyignore` — extended with `AVD-AWS-0176` entry (1 new line).

## Commit

- Branch: `feat/v17611-qa-gates-7-repos`
- SHA: `41e2642` (pushed to `origin`).
- PR: `nfsarch33/helixon-platform#34` (extended; was previously just the coverage lift).

## Cross-references

- **CF-v17612-10**: trivy evidence/ false-positive suppression — closed by this work.
- **v17703-4**: Trivy transitive CVE bumps in helix-dev-tools — sibling task.
- **v17704-1**: govulncheck fleet-wide stdlib x509 bump — closes the
  `golang.org/x/sys/windows` finding by bumping go.mod across 6 repos.