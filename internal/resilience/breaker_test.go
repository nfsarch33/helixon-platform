// Tests for the resilience circuit breaker (v17006-1 RED tests).
package resilience

import (
	"errors"
	"testing"
	"time"
)

// TestBreaker_OpensOn5xx asserts the breaker opens after N consecutive
// 5xx-class failures (transport / upstream errors).
func TestBreaker_OpensOn5xx(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "minimax-test",
		FailureThreshold:  3,
		OpenTimeout:       50 * time.Millisecond,
		HalfOpenSuccesses: 1,
	})

	// First 2 failures should NOT trip.
	for i := 0; i < 2; i++ {
		if err := b.RecordFailure(errors.New("502 bad gateway")); err != nil {
			t.Fatalf("attempt %d: want nil (still closed), got %v", i+1, err)
		}
	}
	// 3rd failure → open.
	err := b.RecordFailure(errors.New("502 bad gateway"))
	if !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("3rd failure: want ErrBreakerOpen, got %v", err)
	}
}

// TestBreaker_HalfOpenAfterTimeout asserts the breaker transitions
// open → half-open after the open timeout.
func TestBreaker_HalfOpenAfterTimeout(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "minimax-halfopen",
		FailureThreshold:  2,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 1,
	})

	// Trip it.
	_ = b.RecordFailure(errors.New("502"))
	_ = b.RecordFailure(errors.New("502"))
	if !b.IsOpen() {
		t.Fatal("breaker should be open after 2 failures")
	}

	// Wait for open timeout.
	time.Sleep(20 * time.Millisecond)

	if b.IsOpen() {
		t.Fatal("breaker should transition to half-open after timeout")
	}
}

// TestBreaker_ClosesAfterSuccessfulHalfOpenProbe asserts successful probes
// close the breaker.
func TestBreaker_ClosesAfterSuccessfulHalfOpenProbe(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "minimax-close",
		FailureThreshold:  2,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 2,
	})

	_ = b.RecordFailure(errors.New("502"))
	_ = b.RecordFailure(errors.New("502"))
	if !b.IsOpen() {
		t.Fatal("breaker should be open")
	}

	time.Sleep(20 * time.Millisecond)
	// 2 successes → closed.
	if err := b.RecordSuccess(); err != nil {
		t.Fatalf("1st success: %v", err)
	}
	if err := b.RecordSuccess(); err != nil {
		t.Fatalf("2nd success: %v", err)
	}
	if b.State() != "closed" {
		t.Fatalf("breaker should be closed after 2 successful probes; got %s", b.State())
	}
}

// TestBreaker_FailFastOn4xx asserts 4xx-class errors DO NOT trip the
// breaker (per global rule: never retry 4xx).
func TestBreaker_FailFastOn4xx(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "minimax-failfast",
		FailureThreshold:  2,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 1,
	})

	// 4xx errors should NOT count toward threshold.
	for i := 0; i < 5; i++ {
		if err := b.RecordFailure(ErrBadRequest); err != nil {
			t.Fatalf("attempt %d: 4xx should not trip, got %v", i+1, err)
		}
	}
	if b.IsOpen() {
		t.Fatal("breaker should NOT be open after 4xx-only errors")
	}
}

// TestBreaker_StateTransitions asserts the state name is reported correctly.
func TestBreaker_StateTransitions(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "minimax-state",
		FailureThreshold:  2,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 1,
	})
	if got := b.State(); got != "closed" {
		t.Fatalf("initial state: want closed, got %s", got)
	}
	_ = b.RecordFailure(errors.New("502"))
	_ = b.RecordFailure(errors.New("502"))
	if got := b.State(); got != "open" {
		t.Fatalf("after threshold: want open, got %s", got)
	}
	time.Sleep(20 * time.Millisecond)
	_ = b.IsOpen() // triggers transition
	if got := b.State(); got != "half-open" {
		t.Fatalf("after timeout: want half-open, got %s", got)
	}
}
