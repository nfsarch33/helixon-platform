// Package resilience — integration helper for wrapping upstream calls
// (e.g. MiniMax provider) with circuit-breaker protection.
package resilience

import (
	"errors"
	"time"
)

// WrapCall returns a function that invokes fn through the breaker. On
// upstream 4xx (ErrBadRequest via errors.Is), the call is fail-fast:
// the breaker is not tripped and the error is returned to the caller.
func WrapCall(b *Breaker, fn func() error) func() error {
	return func() error {
		if b.IsOpen() {
			return ErrBreakerOpen
		}
		err := fn()
		if err == nil {
			_ = b.RecordSuccess()
			return nil
		}
		// 4xx fail-fast: propagate error but don't trip.
		if errors.Is(err, ErrBadRequest) {
			return err
		}
		_ = b.RecordFailure(err)
		return err
	}
}

// ProviderCallStats returns stats for a breaker (state + name).
type ProviderCallStats struct {
	State string
	Name  string
}

// Stats returns the breaker's state + name (for metrics emission).
func Stats(b *Breaker) ProviderCallStats {
	return ProviderCallStats{State: b.State(), Name: b.name}
}

// Sentinel 5xx error for tests + external callers.
var (
	ErrUpstream5xx = errors.New("resilience: upstream 5xx")
	ErrUpstream4xx = ErrBadRequest
	ErrUpstreamNet = errors.New("resilience: upstream network")
)

// Classify5xxOr4xx returns true if the error is 5xx-class (retryable) vs 4xx
// (fail-fast). Network errors count as 5xx (transient).
func Classify5xxOr4xx(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrBadRequest) || errors.Is(err, ErrUpstream4xx) {
		return ErrUpstream4xx
	}
	return ErrUpstream5xx
}

// Backoff returns the recommended retry delay (exponential backoff + jitter)
// for retryable 5xx errors. Capped at maxBackoff. Caller should NOT retry
// after attempt N (=3 by default).
func Backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	base := time.Duration(1<<attempt) * 100 * time.Millisecond // 200ms, 400ms, 800ms...
	if base > 5*time.Second {
		base = 5 * time.Second
	}
	return base
}
