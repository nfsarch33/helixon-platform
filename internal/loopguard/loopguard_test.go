// Package loopguard detects repeating tool-call hashes within a sliding time
// window and surfaces ErrLoopDetected when the configurable threshold is
// exceeded. RED tests for v17003-1.
//
// Author/Machine-Id: cursor-parent@win3-wsl3
package loopguard

import (
	"errors"
	"testing"
	"time"
)

// TestLoopGuard_DetectsRepeatingToolCallHash asserts that 3 identical hashes
// within the default window trigger ErrLoopDetected.
func TestLoopGuard_DetectsRepeatingToolCallHash(t *testing.T) {
	lg := New(DefaultThreshold, DefaultWindow)
	hash := "tool:read_file,args:{\"path\":\"/foo\"}"

	if err := lg.Observe(hash); err != nil {
		t.Fatalf("1st Observe: unexpected error: %v", err)
	}
	if err := lg.Observe(hash); err != nil {
		t.Fatalf("2nd Observe: unexpected error: %v", err)
	}
	if err := lg.Observe(hash); err == nil {
		t.Fatal("3rd Observe: expected ErrLoopDetected, got nil")
	}
}

// TestLoopGuard_CircuitBreaksAfter3Repeats asserts the threshold semantics
// (default 3 → break on the 3rd repeat, not the 4th).
func TestLoopGuard_CircuitBreaksAfter3Repeats(t *testing.T) {
	lg := New(3, time.Minute)
	hash := "h1"

	// 1st and 2nd should pass.
	if err := lg.Observe(hash); err != nil {
		t.Fatalf("1st: %v", err)
	}
	if err := lg.Observe(hash); err != nil {
		t.Fatalf("2nd: %v", err)
	}
	// 3rd triggers ErrLoopDetected.
	if err := lg.Observe(hash); !errors.Is(err, ErrLoopDetected) {
		t.Fatalf("3rd: want ErrLoopDetected, got %v", err)
	}
}

// TestLoopGuard_AllowsDistinctHashes asserts that distinct hashes never
// trigger ErrLoopDetected even when many are observed.
func TestLoopGuard_AllowsDistinctHashes(t *testing.T) {
	lg := New(3, time.Minute)
	for i := 0; i < 100; i++ {
		hash := "hash-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i%10))
		if err := lg.Observe(hash); err != nil {
			t.Fatalf("distinct hash %d: unexpected error: %v", i, err)
		}
	}
}

// TestLoopGuard_60sWindowExpiry asserts that observations older than the
// sliding window are forgotten — a hash repeated after 60s+1ns is allowed.
func TestLoopGuard_60sWindowExpiry(t *testing.T) {
	lg := New(3, 60*time.Millisecond) // shrink window for test speed
	hash := "h1"

	_ = lg.Observe(hash)
	_ = lg.Observe(hash)

	// Wait > window.
	time.Sleep(80 * time.Millisecond)

	// After window expiry, the 1st new Observe should NOT trip (only 1 in window).
	if err := lg.Observe(hash); err != nil {
		t.Fatalf("after window: want nil (only 1 in window), got %v", err)
	}
}

// TestLoopGuard_ConfigurableThreshold asserts that threshold and window
// are configurable and respected.
func TestLoopGuard_ConfigurableThreshold(t *testing.T) {
	// threshold=5, window=1min
	lg := New(5, time.Minute)
	hash := "h1"

	for i := 0; i < 4; i++ {
		if err := lg.Observe(hash); err != nil {
			t.Fatalf("Observe %d/4: unexpected error: %v", i+1, err)
		}
	}
	// 5th trips.
	if err := lg.Observe(hash); !errors.Is(err, ErrLoopDetected) {
		t.Fatalf("Observe 5/5: want ErrLoopDetected, got %v", err)
	}
}

// TestLoopGuard_ResetClearsState asserts Reset removes all tracked hashes.
func TestLoopGuard_ResetClearsState(t *testing.T) {
	lg := New(3, time.Minute)
	hash := "h1"
	_ = lg.Observe(hash)
	_ = lg.Observe(hash)
	lg.Reset()
	// After reset, observing again should not trip on the 1st call.
	if err := lg.Observe(hash); err != nil {
		t.Fatalf("after Reset, 1st Observe: want nil, got %v", err)
	}
}

// TestLoopGuard_StatsReturnsTrackedHashes asserts Stats() reports active
// distinct hashes (after window pruning).
func TestLoopGuard_StatsReturnsTrackedHashes(t *testing.T) {
	lg := New(3, time.Minute)
	_ = lg.Observe("a")
	_ = lg.Observe("b")
	_ = lg.Observe("a") // same hash, distinct times
	if n := lg.Stats(); n != 2 {
		t.Fatalf("Stats: want 2 distinct hashes, got %d", n)
	}
}

// TestLoopGuard_NewDefaultsOnInvalidArgs asserts New() substitutes defaults
// when given non-positive args (defensive coding).
func TestLoopGuard_NewDefaultsOnInvalidArgs(t *testing.T) {
	lg := New(0, 0) // both invalid
	if lg.threshold != DefaultThreshold {
		t.Fatalf("threshold default not applied: %d", lg.threshold)
	}
	if lg.window != DefaultWindow {
		t.Fatalf("window default not applied: %v", lg.window)
	}
}
