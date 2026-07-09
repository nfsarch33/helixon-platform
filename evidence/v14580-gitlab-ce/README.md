# v14580 — GitLab CE bring-up on wsl1 (k3s or rootless Podman)

## Sprint goal

Per `v14576-v14593_deferred_cf_+_production-ready_27dc4bb3.plan.md` Pair 3 MVP, install GitLab CE
on wsl1 (Helixon central node), unblocking CF-v14572-01 (gitlab-runner registration).

The plan offered **two paths**: (a) Helm chart into the k3s `gitlab` namespace, OR (b) rootless
Podman container. This sprint chose **(b) Podman** because:
- The plan forbids docker (operator directive).
- Helm + GitLab CE chart is heavy and requires tiller-less Helm 3 + cert-manager.
- Podman rootless matches the existing observability stack deployment pattern.

## Vendor verification

| Source                                 | Reference                          |
|----------------------------------------|------------------------------------|
| `gitlab/gitlab-ce` Docker Hub          | `docker.io/gitlab/gitlab-ce:latest` |
| Manifest digest                        | `sha256:ed48f1d320841a393c3b8c3a9558f28117a4ebdbb35538b43a73467aabd9590a` |
| Image size                             | ~3.6 GB                            |
| Pulled on wsl1                         | 2026-07-09 17:39 (sha256:315fa7d5...) |

GitLab CE is the official upstream community edition image (not a fork). Pulled via podman with
the docker.io registry.

## Configuration

### 1Password root password (UUID `m5rnja2b2qtsonin6ehy3i7nha`)
- Title: `GitLab CE Root UI (wsl1)`
- Vault: `Cursor_IronClaw`
- Length: 32 chars
- sha256: `f5bf5a955fbc57333178d3e20d63ca9db901f155e9fe2ea78d0b5c5de9e5432f`

Loaded at runtime via `op item get` and passed to `gitlab_rails['initial_root_password']` in the
GITLAB_OMNIBUS_CONFIG env var.

### Persistent volumes
| Host path                                 | Container path     |
|-------------------------------------------|--------------------|
| `/home/jaslian/data/gitlab/config`        | `/etc/gitlab`      |
| `/home/jaslian/data/gitlab/logs`          | `/var/log/gitlab`  |
| `/home/jaslian/data/gitlab/data`          | `/var/opt/gitlab`  |

### Container resource limits
- `--memory=6g`
- `--cpus=4`
- Port mappings: `8929:8929` (HTTP), `2224:22` (SSH), `9443:443` (HTTPS)

### gitlab.rb append (after init)
```ruby
# v14580 - fixed puma threads (default min 4 must be <= max)
puma['min_threads'] = 2
puma['max_threads'] = 8

# v14580 - lightweight resource tuning
postgresql['shared_buffers'] = '256MB'
sidekiq['max_concurrency'] = 5
prometheus_monitoring['enable'] = false
nginx['listen_port'] = 8929
```

## Attempt history

### Attempt 1 — first launch
- Result: image pulled (315fa7d5...), container started, port mappings live.
- Failure: puma crashed with `The minimum (4) number of threads must be less than or equal to the max (2)`.
- Root cause: GITLAB_OMNIBUS_CONFIG included `puma['workers'] = 2; puma['max_threads'] = 2`,
  conflicting with the omnibus default `min_threads = 4`. Fix: set explicit min/max.

### Attempt 2 — puma fix
- Appended `puma['min_threads'] = 2; puma['max_threads'] = 8` to gitlab.rb.
- Result: container started but **postgresql failed with `Permission denied` on PG_VERSION**.
- Root cause: rootless podman mounts the volume with host UID:GID. The gitlab image's postgres
  user (UID 999) couldn't read the host-owned PG_VERSION file (host UID 1001).

### Attempt 3 — re-chown data dir to UID 1000
- `chown -R 1000:1000 /home/jaslian/data/gitlab/data` → failed because files were owned by
  user `ubuntu:lxd` (left over from earlier `sudo mkdir` by the ubuntu user).
- Force `chown -R 1001:1001` (host user jaslian).
- Result: container started, but **Gitaly disk cache + PG_VERSION permission denied** again.
  The gitlab image's Gitaly process runs as a separate UID; rootless podman's UID mapping
  (`subuid: 165536:65536` for jaslian) does not cover UID 1000 or 999 inside the container.

### Attempt 4 — `--userns=keep-id`
- Container started with `d880cc53...`, but the post-init `podman ps -a` hung for >7 minutes.
- All subsequent `podman ps` calls hung. `podman system migrate` was required to recover.

### Attempt 5 — post-migrate
- Container exited with `(1)` status. System state restored (other containers running normally).
- Ports 8929 / 2224 now closed.

## Verdict

**v14580 PARTIALLY COMPLETE**. GitLab CE image is downloaded and the canonical configuration is
proven via three failed attempts. The full installation is **blocked** by WSL2 rootless-podman
permission-mapping constraints:

1. The gitlab image uses several UIDs (gitlab=1000, gitlab-www=998, redis=997, postgres=999,
   gitaly=996). Rootless podman with subuid `165536:65536` cannot map all of these to host UIDs.
2. The `:U` and `:Z` volume flags apply SELinux relabeling but don't fix UID mismatches.
3. `--userns=keep-id` works for matching UIDs but requires the host UID to be 1000 (it's 1001),
   which can't be remapped.

## Carry-forward — CF-v14580-01

**Pivot to Helm + k3s deployment** for the v14581 sprint. Specifically:
1. Install `helm` (v3.14+) via the official binary (NOT a Docker image — vendor verify
   `helm/helm`).
2. Use the official `gitlab/gitlab` Helm chart (NOT `gitlab/gitlab-ee` chart, since we need CE).
3. Deploy into a new k3s namespace `gitlab` with external PostgreSQL and Redis (use bitnami
   charts for those).
4. Allocate at least 8GB RAM and 4 vCPU to the chart.
5. Expose via NodePort (8929 HTTP, 2224 SSH) on wsl1.

This is the standard, supported GitLab-on-k8s deployment path and avoids the rootless-podman
UID-mapping issue entirely.

## Evidence captured

- `podman-run.txt` — first launch output
- `podman-rerun.txt` — second launch output
- `podman-keepid.txt` — fourth launch output
- `podman-final.txt` — fifth launch output
- `podman-ps-30s.txt` / `podman-ps-2min.txt` / `podman-ps-3min.txt` / `podman-ps-5min.txt` — podman ps snapshots
- `podman-logs-2min.txt` / `podman-logs-3min.txt` / `podman-logs-5min.txt` — container logs
- `data-dir.txt` — initial data dir layout
- `gitlab.rb` (host file) — final config
- `svcregistry-register.json` / `svcregistry-list.json` — svcregistryd entry (status=down)
- `1p-item.json` — root password fingerprint

## svcregistryd registration

Registered `gitlab-ce` service as `down` (since container is not healthy):
```json
{"name":"gitlab-ce","host":"127.0.0.1","port":8929,"protocol":"http","owner":"v14580","status":"down","tailscale_ip":"100.84.108.92"}
```

This will auto-update to `up` once the Helm deployment in v14581 succeeds.

## Verification

- [x] Image vendor verified (`gitlab/gitlab-ce`, no fork)
- [x] Pulled image (sha256:315fa7d5...) → evidence captured
- [x] Container launched multiple times
- [x] Root password loaded from 1Password UUID
- [x] puma thread fix identified (min 4 must be ≤ max)
- [x] gitlab.rb config persists in /home/jaslian/data/gitlab/config
- [ ] Container fails to fully initialize — **blocked**
- [ ] HTTP 200 on http://127.0.0.1:8929/-/health — **NOT achieved**
- [ ] gitlab-rails console accessible — **NOT achieved**

**Carry-forward**: CF-v14580-01 (Helm + k3s deployment) created. v14581 will pivot accordingly.