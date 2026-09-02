# ADR-0004: Where the operator console lives, and how it is served

- Status: accepted (v18809, S4 of the 24h usability programme)
- Date: 2026-09-03
- Estate record: cursor-global-kb ADR-092 (this file is the public-repo copy; it carries no estate detail)

## Context

The programme requires a setup wizard and an operator console that a
non-technical person can install and use, plus a one-command install (S6).
The UI stack is a locked decision: React + Next.js for every Helixon UI, with
SSR mandatory for public and multi-tenant surfaces and a static export
explicitly allowed for simple, non-public pages when the choice is recorded.
The operator console v1 is a single-operator surface bound to loopback next
to the agent's own read API; it is neither public nor multi-tenant.

Two homes were possible: a separate web repository with its own Node
runtime, or a `web/console` application in this repository built to a
static export and embedded into the `helixon` binary.

## Decision

1. The console lives in this repository under `web/console`: Next.js (the
   estate reference stack: Next 16.x, React 19, Tailwind 4, SWR, Vitest,
   Playwright), one component kit, `ui-ux-pro-max-skill` for design work.
2. Console v1 is built with `output: "export"` (pre-rendered HTML per route)
   and embedded into the `helixon` binary with `go:embed` behind the build
   tag `console`; `helixon serve --dashboard-addr` serves it at `/console/`
   next to the read API it renders (`/api/v1/runs`, `/api/v1/costs`,
   `/api/v1/evals`, `/api/v1/memory/search`, plus the existing
   `/api/v1/dashboard`). Without the tag the route answers with a clear
   "rebuild with -tags console" message rather than a blank page. This is
   the "simple/static" option the stack rule permits for a non-public
   surface, chosen for the one-artifact install: no Node runtime on the
   operator's machine.
3. The moment the console becomes a public or multi-tenant surface (the
   platform admin UI), it moves to SSR (`output: "standalone"`) behind the
   same API and adopts the shared multi-tenant auth package the stack rule
   names as the first deliverable of that work. Nothing in v1's code depends
   on the export mode.

## Consequences

- One artifact to install; the UI is versioned and released with the API it
  reads; the existing static-export + `go:embed` + build-tag pattern in the
  estate is reused rather than a new one invented.
- The console renders live backend data only: every panel maps to one
  endpoint above, and absence (no runs yet, no cycle yet, memory not
  configured) is rendered as absence, never as placeholder data.
- Access control in v1 is the loopback bind of the dashboard address; the
  approval flows of S5 add an operator token on the mutating endpoints
  before any of them exist.
