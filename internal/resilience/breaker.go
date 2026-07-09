// Package resilience provides a circuit breaker for upstream LLM/3rd-party
// HTTP providers. It is intentionally minimal (no third-party dependency)
// and is safe for concurrent use.
//
// State machine:
//
// closed    --[N consecutive failures]--> open
// open      --[OpenTimeout elapses]------> half-open
// half-open --[HalfOpenSuccesses successes]-> closed
// half-open --[1 failure]----------------> open
//
// 4xx-class errors are fail-fast: they are surfaced to the caller but
// do NOT count toward the failure threshold (per global rule: never
// retry 4xx).
package resilience

import (
	"errors"
	"sync"
	"time"
)

// ErrBreakerOpen is returned when the breaker is in the Open state and
// the caller should fail-fast without dispatching the upstream call.
var ErrBreakerOpen = errors.New("resilience: breaker is open")

// ErrBadRequest is the canonical 4xx-class sentinel. Callers wrap their
// upstream errors and pass to RecordFailure; the breaker ignores this class.
var ErrBadRequest = errors.New("resilience: bad request (4xx — fail-fast, not counted)")

// BreakerConfig configures the breaker.
type BreakerConfig struct {
	Name              string
	FailureThreshold  int           // consecutive 5xx-class failures to open
	OpenTimeout       time.Duration // time in Open before transitioning to half-open
	HalfOpenSuccesses int           // consecutive successes in half-open to close
	Now               func() time.Time
}

// Breaker is a per-upstream circuit breaker.
type Breaker struct {
	mu             sync.Mutex
	name           string
	threshold      int
	openTimeout    time.Duration
	halfOpenNeeded int
	now            func() time.Time

	state     state
	failures  int
	openUntil time.Time
	halfOpens int
}

type state int

const (
	stateClosed state = iota
	stateOpen
	stateHalfOpen
)

// NewBreaker constructs a breaker with sane defaults applied.
func NewBreaker(cfg BreakerConfig) *Breaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = 30 * time.Second
	}
	if cfg.HalfOpenSuccesses <= 0 {
		cfg.HalfOpenSuccesses = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Breaker{
		name:           cfg.Name,
		threshold:      cfg.FailureThreshold,
		openTimeout:    cfg.OpenTimeout,
		halfOpenNeeded: cfg.HalfOpenSuccesses,
		now:            cfg.Now,
		state:          stateClosed,
	}
}

// RecordFailure reports a failure. Returns ErrBreakerOpen if this failure
// trips the breaker; returns nil if accepted (still closed).
//
// 4xx-class errors (matching ErrBadRequest via errors.Is) do NOT count.
func (b *Breaker) RecordFailure(err error) error {
	if errors.Is(err, ErrBadRequest) {
		// Fail-fast per global rule. Caller should NOT retry.
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	switch b.state {
	case stateClosed:
		b.failures++
		if b.failures >= b.threshold {
			b.state = stateOpen
			b.openUntil = now.Add(b.openTimeout)
			b.failures = 0
			return ErrBreakerOpen
		}
		return nil
	case stateOpen:
		// Already open; extend open timeout.
		b.openUntil = now.Add(b.openTimeout)
		return ErrBreakerOpen
	case stateHalfOpen:
		// Probe failed; re-open.
		b.state = stateOpen
		b.openUntil = now.Add(b.openTimeout)
		b.halfOpens = 0
		return ErrBreakerOpen
	}
	return nil
}

// RecordSuccess reports a success. Transitions the breaker toward closed.
// In Closed state: resets failure counter. In HalfOpen: counts toward close threshold.
// In Open: no-op (caller should not have called).
func (b *Breaker) RecordSuccess() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Auto-transition from Open to HalfOpen if timeout has elapsed.
	if b.state == stateOpen && b.now().After(b.openUntil) {
		b.state = stateHalfOpen
		b.halfOpens = 0
	}

	switch b.state {
	case stateClosed:
		b.failures = 0
		return nil
	case stateHalfOpen:
		b.halfOpens++
		if b.halfOpens >= b.halfOpenNeeded {
			b.state = stateClosed
			b.failures = 0
			b.halfOpens = 0
		}
		return nil
	case stateOpen:
		// Still open (timeout not yet elapsed).
		return ErrBreakerOpen
	}
	return nil
}

// IsOpen returns true if the breaker is currently preventing dispatch
// (Open or HalfOpen with active openUntil).
func (b *Breaker) IsOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == stateOpen {
		if b.now().After(b.openUntil) {
			// Transition to half-open.
			b.state = stateHalfOpen
			b.halfOpens = 0
			return false
		}
		return true
	}
	return false
}

// State returns the current breaker state name (closed/open/half-open).
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case stateClosed:
		return "closed"
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	}
	return "unknown"
}
