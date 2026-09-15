# MIGRATION v18839 — removal of the unused control-plane heartbeat monitor

## What was removed

`internal/helixon/controlplane/heartbeat.go`, and with it `HeartbeatConfig`,
`HeartbeatPayload`, `HeartbeatSink`, `A2AHeartbeatSink`, `NewA2AHeartbeatSink`,
`LogHeartbeatSink`, `HeartbeatMonitor`, `NewHeartbeatMonitor`, `Start`, `Update`,
`SetExtra`, `Uptime`, `SendNow`, `SendShutdown` and `FleetDailyReport`.

## Who is affected

Nobody. The package sits under `internal/`, so an out-of-module consumer is
impossible by construction. Within the module every one of those symbols had
zero production callers; the only references were the package's own tests, which
are removed in the same change.

## Why removed rather than fixed

The sink posted to an endpoint the control plane does not serve. Its tests
passed only because they asserted against an in-memory double, never against the
route — so the component was green in CI and non-functional in production. This
package has already set the precedent of deleting such a client rather than
deprecating it, on the reasoning that a client green in CI but failing in
production is worse than no client at all.

`FleetDailyReport` went with it: its only caller was its own test, and it
duplicates the daily report in `internal/helixon/fleet/report.go` that
`internal/helixon/fleet/email.go` actually sends.

## Agent liveness is unaffected

The heartbeat that actually runs is the agent runtime's own loop, which reports
liveness through the registration call in
`internal/helixon/controlplane/sprintboard.go`. It never used any removed symbol.

## Rollback

`git revert` of the commit restores the file, its tests and the two doc
references in one step. No data migration, no schema change, no configuration
change and no redeploy is involved, so a revert is complete and immediate.
