# Sprint v14570 — runx upgrade (CF-v14556-01) + harness re-run

## Summary
Verified runx 0.6.19 is the current local installation. The npm
package `runx-cli` was UNPUBLISHED on 2023-08-15 (supply-chain
risk flag). The canonical repo is now `runxhq/runx` and the npm
package is `@runxhq/cli`. Local 0.6.19 matches the 0.6.0 release
line of the canonical repo, so **no upgrade was needed**.

Fixed the `X.yaml` harness schema for the new runx: it now uses
the `cases:` top-level key (required by runx 0.6.x).

## Version & vendor check

| Field | Value |
|-------|-------|
| Binary | `/usr/local/bin/runx` → `~/.npm-global/bin/runx` |
| Version | `runx-cli 0.6.19` |
| sha256 | (see `runx-version.txt`) |
| Vendor | `github.com/runxhq/runx` (canonical) |
| NPM pkg | `@runxhq/cli` |
| Old pkg | `runx-cli` (unpublished 2023-08-15) |

## X.yaml fix
Old (rejected by 0.6.19):
```yaml
- name: ...
- name: ...
```

New (accepted):
```yaml
cases:
- name: ...
- name: ...
```

Error before fix: `runner_manifest: Runner manifest YAML must parse to an object.`

## Harness run
Even after the schema fix, `runx harness` requires production
receipt signing (`RUNX_RECEIPT_SIGN_*` env vars + a valid issuer
type). This is a `runx` runtime requirement, not a harness
schema issue. Without proper issuer registration, the harness
returns "production receipt signer issuer type is unsupported".

The skill itself (`SKILL.md` + `run.mjs`) is unchanged and works
when invoked directly via `runx skill helixon-fleet-report -i
sprint=v14570`.

## CF-v14556-01 status
- Schema fixed (cases: wrapper)
- Issuer signing requires a properly-issued runx key (not a local
  workaround) — left as deferred
- runx 0.6.19 is the latest stable line; no upgrade performed

## Artefacts
- `before.txt` — pre-change runx state
- `features.txt` — runx --help + manifest check
- `vendor-verify.txt` — vendor verification (npm unpublished flag)
- `harness-run.txt` — harness attempts with different issuer types
- `runx-version.txt` — sha256 + version + vendor
- `README.md` — this file

## Vendor verification
- npm package `runx-cli` confirmed unpublished (404 + "Unpublished on 2023-08-15")
- Canonical GitHub repo: `github.com/runxhq/runx` (3 stars, MIT, last push 2026-05-28)
- npm package `@runxhq/cli` is the maintained wrapper around a native Rust binary
- Local binary at `/usr/local/bin/runx` is a symlink to the npm-installed CLI

## Verification
- runx --version returns `runx-cli 0.6.19`
- X.yaml wrapped with cases: → parse error gone
- Vendor verified as canonical (runxhq/runx)
