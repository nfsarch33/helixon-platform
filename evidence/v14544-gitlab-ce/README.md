# Sprint v14544 — GitLab CE Helm install + runner

## Summary
Helm-installed GitLab CE on k3s (closed CF-v14536-01) and pivoted to
the lightweight GitLab Runner for v14554.

## Artefacts
- `helixon-platform/k8s/gitlab/runner-values.yaml`
- `helixon-platform/scripts/install-gitlab-runner.sh`
- `cursor-global-kb/sop/gitlab-runner-k3s.md`

## Verification
- `helm list -n gitlab` — gitlab-runner present
