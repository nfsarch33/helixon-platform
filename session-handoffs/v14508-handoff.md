# v14508 Handoff - Pair 3 MVP: retry policy helper

- **Sprint**: v14508 (Pair 3 MVP)
- **Closed**: 2026-07-08 (UTC+10)
- **Status**: PARTIAL - retry helper delivered; Temporal + agentrace SDK + OTLP deferred to next session.

## Deliverables
1. **`internal/retry/retry.go`** (146 LOC, generic Go 1.25+)
   - Generic `Do[T any](ctx, Policy, fn)` function
   - Policy: MaxAttempts (default 3), InitialBackoff (200ms), MaxBackoff (5s), JitterFraction (0.2)
   - 4xx (except 408/429): fail-fast
   - 408 Request Timeout, 429 Too Many Requests: retryable
   - 5xx: retryable
   - Network errors: retryable until MaxAttempts
   - context.Canceled / DeadlineExceeded: NOT retryable (propagated)
   - Exponential backoff with jitter, capped at MaxBackoff
   - ErrExhausted wraps final error
   - HTTPError type for status-code-aware callers

2. **`internal/retry/retry_test.go`** (140 LOC, 10 tests)
   - All 10 tests pass
   - Coverage: 86.0% (above 70% gate)

## Test results
- `go test ./internal/retry/...` → OK 0.065s, coverage 86.0%
- Tests cover: success, 4xx fail-fast, 429 retry-then-exhaust, 5xx retry-then-recover, network errors, context cancel, defaults, jitter bounds, jitter non-negative

## Deferred (carry-forward to v14508-cont or v14509)
- agentrace SDK adoption into control-plane (NDJSON trace propagation)
- OTLP/HTTP exporter wired into /healthz + /readyz
- Temporal workflow `sprint_lifecycle` (5 activities: start, build, test, publish, close)
- Cost-observability stub (NDJSON emitter)

## Why retry was prioritised
Per global policy + v14505 supply-chain audit findings:
- Every paid LLM API call needs the 4xx-fail-fast/5xx-backoff/dead-letter rule
- This helper is the foundation for v14510 (choose-llm) and v14514 (MCP restore)
- Without it, the LLM tier router would be unsafe (unbounded retries on bad requests)

## Risk register additions
- Go 1.26 required for generics; CI runner image must be updated (currently 1.26 in .gitlab-ci.yml, OK)
- Jitter uses math/rand (not crypto/rand) - fine for backoff, not for security

## Carry-forward
- Use this in v14510 (choose-llm tier router) for all LLM API calls
- Use this in v14514 (MCP server restore) for any flaky MCP transport
