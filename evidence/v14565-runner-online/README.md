# Sprint v14565 — GitLab Runner re-registration + lint pipeline smoke

## Summary
GitLab Runner was previously installed during v14544 but is
currently not running in the cluster (likely removed during the
v14554 ArgoCD/Helm chart cleanups). Since GitLab CE itself is
also not currently deployed, re-registration is deferred.

This sprint instead:

1. Verified the GitLab Runner Auth Token (1Password UUID
   `n2ecpwlnkpjs4ufdvquw6xf624`) is reachable via `op read`.
2. Validated `ci/sentrux.gitlab-ci.yml` (5 stages, 9 jobs).
3. Ran the local Sentrux audit (same checks the GitLab CI
   `sentrux:audit` stage would run).

## 1Password item (UUID lookup, per rule 01-1password-uuid-required)
- Item: `GitLab CE Runner Auth Token (wsl2)` UUID `n2ecpwlnkpjs4ufdvquw6xf624`
- Stored in: `notesPlain` (not `password`) field
- Token prefix: `glrt-HWa3UDzgdUutaPV...`
- Token length: 56 chars
- sha256: `7d5080c1fae39841d6375b0c8aeea3f5b64fa4618633fd46530d30b5b955dffb`

## CI lint
```
Stages: ['lint', 'test', 'scan', 'sentrux', 'publish']
Jobs: 9
YAML: OK
```

## Local Sentrux audit (mimics CI sentrux:audit stage)
```
verdict: FAIL
check_count: 9, pass_count: 7, fail_count: 1, warn_count: 1
- no-direct-main-push: PASS
- no-plaintext-secrets: PASS
- registry-sot-required: PASS
- llm-router-token-required: WARN
- engram-embed-url-required: PASS
- evidence-directories-present: PASS (17 dirs)
- retro-present: PASS
- adrs-present: PASS
- no-uncommitted-changes: FAIL (1 uncommitted)
```

The single FAIL is the evidence dir being added during this
sprint (will pass on commit). The WARN on llm-router token is
not blocking.

## GitLab CE status
- Not currently running on any fleet node
- wsl2:8929 not reachable
- Token stored safely in 1Password; will be used when GitLab CE
  is re-deployed (separate carry-forward, not in this arc)

## Artefacts
- `runner-status.json` — token fingerprint + audit results
- `discovery.txt` — network probe + lint validation
- `README.md` — this file

## Verification
- 1Password UUID lookup works (token retrieved via op CLI)
- ci/sentrux.gitlab-ci.yml parses cleanly
- Local Sentrux audit returns 7/9 PASS, 1 WARN, 1 FAIL (transient)
