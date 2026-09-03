# The operator console

The console is the read-only view of one running Helixon agent: its runs, what
each run did, what it cost, how the evaluation cycles are trending, and what is
in memory. It renders what the agent's own API returns and nothing else - an
empty list stays empty, an unreachable endpoint says so.

## How it reaches the agent

It is a **static export** (`output: "export"`, `basePath: "/console"`) embedded
into the `helixon` binary and served by the agent at `/console/`. There is no
Node process in production and no separate origin: the pages call
`/api/v1/...` on the same host they were served from.

That home is argued in `docs/adr/0004-operator-console-home.md`. Short version:
the dashboard listener is a loopback, single-operator surface, so a static
export is the whole of what is needed; the day the console is exposed to more
than one person it moves to SSR behind the shared auth package, and the ADR
says what that costs.

## Running it against a live agent

    npm install
    npm run dev            # http://localhost:3000/console/

`next dev` serves under the same basePath, so **`http://localhost:3000/` is a
404 by design** - open `/console/`. Point it at a running agent with

    NEXT_PUBLIC_HLXN_API_BASE=http://127.0.0.1:9410 npm run dev

Without that, `fetch` is same-origin and the dev server has no API to answer.

## Building the console into the binary

    npm run build:embed                       # in web/console
    go build -tags console ./cmd/helixon      # from the repo root

`build:embed` writes the export to `cmd/helixon/consolefs/out/`, which is
**gitignored on purpose**: a checked-in build artefact goes stale silently,
and a stale console is worse than none. The consequence is deliberate and
worth knowing: `go build -tags console` on a fresh checkout fails with

    pattern all:out: no matching files found

That is the intended failure. It is loud, it cannot ship by accident, and the
fix is the `build:embed` line above. A default build (no tag) needs none of
this - it compiles without the console and serves an explanatory 404 at
`/console/`, so the absence is never a blank page.

## Checks

    npm run typecheck      # tsc --noEmit
    npm run lint           # eslint (next lint is gone in Next 16)
    npm test               # vitest, with coverage thresholds enabled
    npm run e2e            # playwright, against a served build

CI runs all of these plus `go test -tags console ./cmd/helixon/consolefs/`,
which serves the freshly built export through the Go handler and asserts the
real assets come back - the only step that can tell a binary carrying the
console from one carrying nothing.

## Layout

    src/app/            one directory per route (overview, runs, runs/detail, evals, costs, memory)
    src/lib/api.ts      the typed client and the only place fetch is called
    src/lib/hooks.ts    SWR wrappers with the polling intervals
    src/components/     Panel, StatusBadge, and the empty/error/loading states
