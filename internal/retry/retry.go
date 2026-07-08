// Package retry provides a configurable retry policy for HTTP/network calls.
//
// Policy:
//   - 4xx (client errors, except 408/429): fail-fast (1 attempt, no retry)
//   - 408 Request Timeout, 429 Too Many Requests: retryable (up to max)
//   - 5xx (server errors): retryable (up to max)
//   - Network errors / context cancel: retryable until context expires
//
// Backoff: exponential with jitter, ceiling = 3 attempts by default.
// After max retries, the error is wrapped with `retry: exhausted` prefix and returned.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

// Policy describes retry behavior.
type Policy struct {
	// MaxAttempts is the total number of attempts (including the first). Default: 3.
	MaxAttempts int
	// InitialBackoff is the wait time before the 2nd attempt. Default: 200ms.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential backoff growth. Default: 5s.
	MaxBackoff time.Duration
	// JitterFraction is the +/- randomization applied to backoff. Default: 0.2 (20%).
	JitterFraction float64
	// HTTPStatusRetryable is an optional override; if nil uses default mapping.
	HTTPStatusRetryable func(status int) bool
}

// withDefaults applies default values.
func (p Policy) withDefaults() Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = 200 * time.Millisecond
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 5 * time.Second
	}
	if p.JitterFraction <= 0 || p.JitterFraction > 1 {
		p.JitterFraction = 0.2
	}
	if p.HTTPStatusRetryable == nil {
		p.HTTPStatusRetryable = DefaultHTTPStatusRetryable
	}
	return p
}

// DefaultHTTPStatusRetryable returns true for HTTP statuses that should be retried.
//   - 408 (Request Timeout) and 429 (Too Many Requests) are retryable client errors
//   - 5xx are retryable server errors
//   - All other 4xx are NOT retryable (fail-fast)
func DefaultHTTPStatusRetryable(status int) bool {
	switch {
	case status == http.StatusRequestTimeout: // 408
		return true
	case status == http.StatusTooManyRequests: // 429
		return true
	case status >= 500 && status <= 599:
		return true
	default:
		return false
	}
}

// ErrExhausted wraps the last error when MaxAttempts is reached.
var ErrExhausted = errors.New("retry: exhausted")

// Do executes fn under the retry policy.
//   - fn is called with attempt number (1-indexed) and the context
//   - fn returns (result, error); a non-nil error triggers retry-or-fail
//   - context cancellation is propagated immediately
//   - sleeps are skipped when ctx is already past deadline
func Do[T any](ctx context.Context, p Policy, fn func(ctx context.Context, attempt int) (T, error)) (T, error) {
	var zero T
	p = p.withDefaults()

	var lastErr error
	backoff := p.InitialBackoff

	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		// Check context first; don't waste an attempt on a dead context
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return zero, fmt.Errorf("%w (ctx cancelled on attempt %d): %w", ErrExhausted, attempt, lastErr)
			}
			return zero, ctx.Err()
		default:
		}

		result, err := fn(ctx, attempt)
		if err == nil {
			return result, nil
		}
		lastErr = err

		// Don't sleep after the last attempt
		if attempt == p.MaxAttempts {
			break
		}

		// Fail-fast on non-retryable errors
		if !isRetryable(err, p) {
			return zero, err
		}

		// Sleep with jitter
		sleep := jitter(backoff, p.JitterFraction)
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return zero, fmt.Errorf("%w (ctx cancelled mid-retry): %w", ErrExhausted, ctx.Err())
		}

		// Exponential backoff: 200ms, 400ms, 800ms, ...
		backoff *= 2
		if backoff > p.MaxBackoff {
			backoff = p.MaxBackoff
		}
	}

	return zero, fmt.Errorf("%w after %d attempts: %w", ErrExhausted, p.MaxAttempts, lastErr)
}

// HTTPError is returned by callers that need to expose the HTTP status code
// to the retry policy.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d %s: %s", e.StatusCode, e.Status, e.Body)
}

// isRetryable inspects err for known types. Currently:
//   - *HTTPError: defer to Policy.HTTPStatusRetryable(status)
//   - context.Canceled / context.DeadlineExceeded: not retryable here (caller should propagate)
//   - everything else: retryable (assumed transient network/timeout)
func isRetryable(err error, p Policy) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return p.HTTPStatusRetryable(httpErr.StatusCode)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// jitter returns d +/- fraction*d, never less than 0.
func jitter(d time.Duration, fraction float64) time.Duration {
	if fraction <= 0 {
		return d
	}
	span := float64(d) * fraction
	// rand.Int63n returns in [0, span*2); we offset by -span to get +/- span.
	delta := time.Duration(rand.Int63n(int64(span*2))) - time.Duration(span)
	out := d + delta
	if out < 0 {
		return 0
	}
	return out
}