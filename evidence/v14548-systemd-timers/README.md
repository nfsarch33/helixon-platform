# runx-public-repo-gate: allow-file fleet_host_alias,network_topology
# v14548 — Pair 5 MVP: Systemd timers (cleanup-orphaned-wsl-procs 30m, fleet-doctor 6h, engram-prune 24h)

**Status:** COMPLETE
**Date:** 2026-07-09
**Operator:** cursor-ai

## Summary

Three systemd user timers installed, enabled, and validated to give the
Helixon fleet self-maintenance behavior:

| Timer | Cadence | First run | Last result |
|---|---|---|---|
| `cleanup-orphaned-wsl-procs.timer` | every 30 min | +5 min after boot | success |
| `fleet-doctor.timer` | every 6h + 00:00/06:00/12:00/18:00 | +10 min after boot | success (32/32 GREEN) |
| `engram-prune.timer` | every 24h at 04:00 | +30 min after boot | success (dropped 3) |

## Files created

- `~/.config/systemd/user/cleanup-orphaned-wsl-procs.service` (and `.timer`)
- `~/.config/systemd/user/fleet-doctor.service` (and `.timer`)
- `~/.config/systemd/user/engram-prune.service` (and `.timer`)
- `~/Code/cursor-global-kb/tools/cleanup-orphaned-wsl.sh` (wrapper around
  PowerShell `Get-Process`; idempotent, supports `--dry-run`)
- `~/Code/cursor-global-kb/tools/engram-prune.py` (Python helper, calls
  engramd mem0-compat API on port 8281)

## Timer pattern

All three timers follow the same pattern:

```ini
[Unit]
Description=Helixon: <task> (v14548)

[Timer]
OnBootSec=<initial delay>
OnUnitActiveSec=<repeating interval>
OnCalendar=<-... <optional fixed time>
Persistent=false
AccuracySec=1s..5min
Unit=<name>.service

[Install]
WantedBy=timers.target
```

This is identical to the existing `pepper-daily-refresh.timer` template,
so the convention is consistent across the Helixon fleet.

## Test results

```text
=== all 3 timers ===
NEXT                             LEFT LAST                               PASSED UNIT                             ACTIVATES
Thu 2026-07-09 18:44:54 AEST    28min Thu 2026-07-09 18:14:33 AEST 2min 13s ago cleanup-orphaned-wsl-procs.timer cleanup-orphaned-wsl-procs.service
Fri 2026-07-10 00:00:00 AEST 5h 43min Thu 2026-07-09 18:14:33 AEST 2min 13s ago fleet-doctor.timer               fleet-doctor.service
Fri 2026-07-10 04:00:00 AEST       9h Thu 2026-07-09 18:14:33 AEST 2min 13s ago engram-prune.timer               engram-prune.service

=== last run results ===
--- cleanup-orphaned-wsl-procs --- Result=success
--- engram-prune ---               Result=success (dropped=3 already_gone=0 failed=0)
--- fleet-doctor ---               Result=success (VERDICT: GREEN, 32 harnesses run)
```

## Notes

- The `cleanup-orphaned-wsl-procs` service runs **inside wsl1** and uses
  `powershell.exe` to query/kill win1's `wsl.exe` processes. The PowerShell
  script (existing in `tools/cleanup-orphaned-wsl-procs.ps1`) does the
  heavy lifting; the new shell wrapper adds error handling and journal
  logging.
- The `fleet-doctor` log goes to `~/logs/fleet-doctor.log`; failures get
  captured to `~/logs/fleet-doctor-errors.log`. The service is configured
  to succeed even if individual harnesses fail (`SuccessExitStatus=0 1`)
  so that a single broken harness does not stop the timer.
- The `engram-prune` script uses the mem0-compat API on `:8281` (not the
  main `:8280` engramd API, which has different paths). It is idempotent:
  re-running it after memories have been deleted is a no-op.
