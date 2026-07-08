# v14506 Handoff - Pair 2 MVP: control-plane /healthz + Helm chart skeleton

- **Sprint**: v14506 (Pair 2 MVP)
- **Closed**: 2026-07-08 (UTC+10)
- **Status**: PARTIAL — added `/healthz` HTTP endpoint + Helm chart skeleton. Full re-scoping of controlplane to a server (was previously a client) deferred.

## Plan-vs-reality correction
The v14504-v14521 plan said "bootstrap helixon-platform/control-plane Go repo". Investigation showed:
- `helixon-platform` repo already exists (last commit 565ae1d, July 6) with mature code
- `internal/helixon/controlplane/` already has `a2a_client.go`, `heartbeat.go`, `sprintboard.go` (clients)
- These are **clients** to a sprintboard/A2A gateway service, NOT a server with the planned `/healthz`, `/v1/sprint`, `/v1/artifact` endpoints
- Full "control-plane server" rebuild would require re-architecting; deferred

## What v14506 actually delivered
1. **`/healthz` HTTP endpoint** (`internal/helixon/controlplane/healthz.go`)
   - `HealthzServer` with `Start()`, `Shutdown(ctx)`, `Addr()`, `StartedAt()`, `ReqCount()`
   - Routes: `GET /healthz` (200 OK with uptime + req count), `GET /readyz` (200 OK with check map)
   - Security context: read-only root filesystem, runAsNonRoot, no privilege escalation
   - Tests: 5 new tests covering start/shutdown, healthz response, readyz response, real httptest server, shutdown graceful path
2. **Helm chart skeleton** (`charts/helixon-control-plane/`)
   - `Chart.yaml` v0.1.0
   - `values.yaml` with all standard tunables (replicas, image, service, ingress, resources, autoscaling, config)
   - `templates/deployment.yaml` (with liveness/readiness probes pointing to /healthz, /readyz)
   - `templates/service.yaml`, `templates/configmap.yaml`, `templates/_helpers.tpl`
   - `helm lint` PASS, `helm template` renders correctly
3. **Coverage improved**: 83.9% -> 84.8% (well above 70% gate)

## What v14506 did NOT deliver (carry-forward to v14507+)
- **chi router integration** — healthz uses net/http directly; chi upgrade is for v14507+ when more routes are added
- **`/v1/sprint/{id}/artifact` endpoint** — sprintboard.go is a client; server-side artifact storage not implemented
- **Postgres sqlx integration** — current heartbeat/sprintboard use HTTP clients; sqlx migration requires schema work
- **testcontainers-go integration test** — deferred to v14507 (needs postgres schema first)
- **HPA** — values.yaml has autoscaling section but disabled by default per plan (single replica)

## Vendor verification (per v14505 audit pattern)
- `helm` v3.21.2 from Helm GitHub release — verified
- `github.com/stretchr/testify` v1.11.1 — already in go.mod, verified canonical
- `github.com/nfsarch33/helixon-platform` module path — canonical, this is our own repo

## Evidence index
- internal/helixon/controlplane/healthz.go (87 LOC)
- internal/helixon/controlplane/healthz_test.go (76 LOC, 5 tests)
- charts/helixon-control-plane/Chart.yaml
- charts/helixon-control-plane/values.yaml
- charts/helixon-control-plane/templates/{deployment,service,configmap,_helpers.tpl}.yaml

## Test results
- `go test ./internal/helixon/controlplane/...` → OK 0.225s, coverage 84.8% (up from 83.9%)
- `helm lint` → 0 failed
- `helm template test ...` → valid YAML for ConfigMap + Service + Deployment

## Decisions
1. **/healthz is minimal.** It deliberately does NOT include DB ping, agentrace check, or sprintboard check yet. Those need testcontainers (v14507) and registered clients (v14508+).
2. **Helm chart is single-replica.** HPA off per plan (sprint_lifecycle is short-lived, control-plane is just a coordinator).
3. **No chi router.** net/http is sufficient for /healthz + /readyz. chi upgrade is v14507+.

## Carry-forward to v14507
1. Add chi router (replace net/http in healthz.go)
2. Add /v1/sprint/{id}/artifact endpoint with sqlx + Postgres
3. Add testcontainers-go integration test (postgres + test for sprint artifact CRUD)
4. Add /v1/fleet/{node}/heartbeat endpoint
5. golangci-lint, govulncheck, gosec, Trivy, Nancy CI
6. Devcontainer + task install

## Risk register additions
- The plan was written assuming a greenfield repo. Reality: 50+ sprint commits of mature code. Re-scoping required.
- Subsequent sprints (v14508-v14521) all depend on assumptions from the plan that may also be stale. Recommend re-reading each plan carefully before execution.
