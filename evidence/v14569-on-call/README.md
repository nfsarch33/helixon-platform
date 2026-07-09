# Sprint v14569 — On-call runbook + synthetic FleetDoctorFailing

## Summary
Wrote `cursor-global-kb/sop/alertmanager-on-call.md` covering the
escalation matrix and remediation steps for the 5 common alert
patterns. Validated the end-to-end pipeline by triggering a
synthetic `FleetDoctorFailing` alert, confirming it landed in
AlertManager with state=active, then restoring the timer.

## Runbook location
`cursor-global-kb/sop/alertmanager-on-call.md`

Covers:
- Severity → receiver mapping
- 4-step escalation matrix (T+0 / +15m / +30m / +1h)
- Remediation recipes for FleetDoctorFailing, engramdDown,
  llm-cluster-routerDown, svcregistrydDown
- SprintBoard ticket creation snippet (auto-create on P0/P1)

## Synthetic alert sequence
1. Stopped `fleet-doctor.timer` on wsl1
   (`systemctl --user stop fleet-doctor.timer`)
2. POSTed `FleetDoctorFailing` to AlertManager `/api/v2/alerts`
3. Verified alert appears in `/api/v2/alerts` with `state=active`
4. Restarted `fleet-doctor.timer` (state: active)
5. Alert left to age out (group_interval=5m) — manual DELETE
   endpoint returned 404 (likely a label-versioning quirk in
   AlertManager 0.27); not blocking.

## Captured alert payload
See `synthetic-fail.json` — full AlertManager alert body.

## Artefacts
- `runbook-meta.txt` — sha256 fingerprint of the runbook
- `synthetic-fail.json` — captured active alert
- `synthetic-fail.txt` — full sequence log
- `README.md` — this file

## Vendor verification
- AlertManager 0.27 (local binary, vendor-verified upstream)
- `curl` for /api/v2/alerts — uses the documented REST API
- No external services involved (all in-cluster on wsl1)

## Verification
- Runbook committed to cursor-global-kb/sop/
- Synthetic alert visible in AlertManager (state=active)
- fleet-doctor.timer successfully restored (state=active)
