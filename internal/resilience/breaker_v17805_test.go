// Copyright 2026 Helixon Platform. SPDX-License-Identifier: MIT.
// Additional resilience tests for v17805-2 (coverage lift).
package resilience

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestNewBreaker_AppliesDefaults asserts every zero-valued field in
// BreakerConfig is replaced with a safe default (FailureThreshold,
// OpenTimeout, HalfOpenSuccesses, Now).
func TestNewBreaker_AppliesDefaults(t *testing.T) {
	b := NewBreaker(BreakerConfig{Name: "defaults"})
	if b.threshold != 3 {
		t.Fatalf("default threshold: want 3, got %d", b.threshold)
	}
	if b.openTimeout != 30*time.Second {
		t.Fatalf("default openTimeout: want 30s, got %v", b.openTimeout)
	}
	if b.halfOpenNeeded != 1 {
		t.Fatalf("default halfOpenNeeded: want 1, got %d", b.halfOpenNeeded)
	}
	if b.now == nil {
		t.Fatal("default Now should be set to time.Now")
	}
	if b.state != stateClosed {
		t.Fatalf("initial state: want closed (stateClosed=%d), got %d", stateClosed, b.state)
	}
}

// TestNewBreaker_RespectsExplicitValues asserts that positive config values
// are kept verbatim and not replaced by defaults.
func TestNewBreaker_RespectsExplicitValues(t *testing.T) {
	custom := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := NewBreaker(BreakerConfig{
		Name:              "explicit",
		FailureThreshold:  7,
		OpenTimeout:       5 * time.Second,
		HalfOpenSuccesses: 4,
		Now:               func() time.Time { return custom },
	})
	if b.threshold != 7 {
		t.Fatalf("explicit threshold lost: want 7, got %d", b.threshold)
	}
	if b.openTimeout != 5*time.Second {
		t.Fatalf("explicit openTimeout lost: want 5s, got %v", b.openTimeout)
	}
	if b.halfOpenNeeded != 4 {
		t.Fatalf("explicit halfOpenNeeded lost: want 4, got %d", b.halfOpenNeeded)
	}
	if got := b.now(); !got.Equal(custom) {
		t.Fatalf("explicit Now lost: want %v, got %v", custom, got)
	}
	if b.name != "explicit" {
		t.Fatalf("name: want explicit, got %s", b.name)
	}
}

// TestRecordFailure_WhileOpenExtendsTimeout asserts that a RecordFailure
// call when the breaker is already Open extends the open timeout and
// still returns ErrBreakerOpen. The fake clock advances by 10ms on each
// invocation so the second openUntil must be strictly later.
func TestRecordFailure_WhileOpenExtendsTimeout(t *testing.T) {
	base := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	var tick int
	b := NewBreaker(BreakerConfig{
		Name:             "open-extend",
		FailureThreshold: 2,
		OpenTimeout:      100 * time.Millisecond,
		Now:              func() time.Time { t := base.Add(time.Duration(tick) * 10 * time.Millisecond); tick++; return t },
	})

	// Trip the breaker (uses Now() twice for the 2 failures; both within threshold window).
	if err := b.RecordFailure(errors.New("502")); err != nil {
		t.Fatalf("1st failure: %v", err)
	}
	if err := b.RecordFailure(errors.New("502")); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("2nd failure should open: %v", err)
	}
	originalUntil := b.openUntil

	// Another failure while still open should re-extend openUntil.
	if err := b.RecordFailure(errors.New("502")); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("failure while open: %v", err)
	}
	if !b.openUntil.After(originalUntil) {
		t.Fatalf("openUntil should advance: was %v, now %v", originalUntil, b.openUntil)
	}
}

// TestRecordFailure_HalfOpenProbeFailsReopens asserts a failure in
// half-open transitions the breaker back to open and zeroes halfOpens.
func TestRecordFailure_HalfOpenProbeFailsReopens(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	b := NewBreaker(BreakerConfig{
		Name:              "halfopen-fail",
		FailureThreshold:  2,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 3,
		Now:               func() time.Time { return now },
	})

	// Trip.
	_ = b.RecordFailure(errors.New("502"))
	_ = b.RecordFailure(errors.New("502"))

	// Advance time past the open timeout.
	now = now.Add(50 * time.Millisecond)
	_ = b.IsOpen() // transitions to half-open
	if b.state != stateHalfOpen {
		t.Fatalf("expected half-open, got state=%d", b.state)
	}
	// Pre-load halfOpens so we can verify the reset.
	b.halfOpens = 2

	// A failure in half-open should re-open and reset halfOpens.
	if err := b.RecordFailure(errors.New("502")); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("half-open failure: %v", err)
	}
	if b.state != stateOpen {
		t.Fatalf("expected open after failed probe, got state=%d", b.state)
	}
	if b.halfOpens != 0 {
		t.Fatalf("halfOpens should reset on re-open, got %d", b.halfOpens)
	}
}

// TestRecordSuccess_OpenBeforeTimeoutNoop asserts that RecordSuccess while
// the breaker is still Open (timeout not elapsed) returns ErrBreakerOpen.
func TestRecordSuccess_OpenBeforeTimeoutNoop(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	b := NewBreaker(BreakerConfig{
		Name:             "open-success",
		FailureThreshold: 1,
		OpenTimeout:      1 * time.Second,
		Now:              func() time.Time { return now },
	})

	_ = b.RecordFailure(errors.New("502"))
	if b.State() != "open" {
		t.Fatalf("want open, got %s", b.State())
	}

	if err := b.RecordSuccess(); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("RecordSuccess while open: %v", err)
	}
	// Still open.
	if b.State() != "open" {
		t.Fatalf("state should remain open, got %s", b.State())
	}
}

// TestRecordSuccess_OpenToHalfOpenTransition asserts that RecordSuccess
// triggers an auto-transition from Open to HalfOpen when the timeout has
// elapsed, and counts the success toward closing.
func TestRecordSuccess_OpenToHalfOpenTransition(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	b := NewBreaker(BreakerConfig{
		Name:              "auto-transition",
		FailureThreshold:  1,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 2,
		Now:               func() time.Time { return now },
	})

	_ = b.RecordFailure(errors.New("502"))

	// Advance time past the open timeout.
	now = now.Add(50 * time.Millisecond)

	if err := b.RecordSuccess(); err != nil {
		t.Fatalf("1st success after timeout: %v", err)
	}
	if b.State() != "half-open" {
		t.Fatalf("after 1st success: want half-open, got %s", b.State())
	}
	// 2nd success closes (HalfOpenSuccesses=2).
	if err := b.RecordSuccess(); err != nil {
		t.Fatalf("2nd success: %v", err)
	}
	if b.State() != "closed" {
		t.Fatalf("after 2nd success: want closed, got %s", b.State())
	}
}

// TestIsOpen_OpenToHalfOpenTransition asserts IsOpen performs the
// auto-transition when timeout elapses.
func TestIsOpen_OpenToHalfOpenTransition(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	b := NewBreaker(BreakerConfig{
		Name:             "isopen-transition",
		FailureThreshold: 1,
		OpenTimeout:      10 * time.Millisecond,
		Now:              func() time.Time { return now },
	})

	_ = b.RecordFailure(errors.New("502"))
	if !b.IsOpen() {
		t.Fatal("want open immediately after trip")
	}

	now = now.Add(50 * time.Millisecond)
	if b.IsOpen() {
		t.Fatal("want half-open (IsOpen=false) after timeout")
	}
	if b.State() != "half-open" {
		t.Fatalf("after IsOpen(): want half-open, got %s", b.State())
	}
}

// TestState_UnknownDefensive ensures State returns "unknown" if the
// internal state field is set to an out-of-range value. Defensive only —
// state should never leave [0,2] in practice.
func TestState_UnknownDefensive(t *testing.T) {
	b := NewBreaker(BreakerConfig{Name: "unknown"})
	b.state = state(99)
	if got := b.State(); got != "unknown" {
		t.Fatalf("unknown state: want unknown, got %s", got)
	}
}

// TestRecordFailure_ConcurrentSafety runs many concurrent RecordFailure /
// RecordSuccess calls to exercise the mutex paths under -race.
func TestRecordFailure_ConcurrentSafety(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "concurrent",
		FailureThreshold:  50,
		OpenTimeout:       1 * time.Second,
		HalfOpenSuccesses: 1,
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			err := b.RecordFailure(fmt.Errorf("concurrent 5xx %d", i))
			_ = err
		}(i)
		go func() {
			defer wg.Done()
			_ = b.RecordSuccess()
		}()
	}
	wg.Wait()
	if b.State() != "closed" && b.State() != "open" && b.State() != "half-open" {
		t.Fatalf("invalid state under concurrency: %s", b.State())
	}
}

// TestRecordFailure_Wrapped4xxNotCounted asserts that a 4xx error wrapped
// via fmt.Errorf("...: %w", ErrBadRequest) is still detected by errors.Is
// and does not count toward the threshold.
func TestRecordFailure_Wrapped4xxNotCounted(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:             "wrapped-4xx",
		FailureThreshold: 2,
		OpenTimeout:      10 * time.Millisecond,
	})
	wrapped := fmt.Errorf("upstream: %w", ErrBadRequest)
	for i := 0; i < 5; i++ {
		if err := b.RecordFailure(wrapped); err != nil {
			t.Fatalf("wrapped 4xx should not trip on attempt %d: %v", i+1, err)
		}
	}
	if b.IsOpen() {
		t.Fatal("breaker should NOT be open after wrapped 4xx only")
	}
}
