// Package loopguard detects repeating tool-call hashes within a sliding time
// window and surfaces ErrLoopDetected when the configurable threshold is
// exceeded. Used by the agent runner to short-circuit runaway tool-call
// loops. GREEN impl for v17003-2.
//
// Author/Machine-Id: cursor-parent@win3-wsl3
package loopguard

import (
	"errors"
	"sync"
	"time"
)

// DefaultThreshold is the default number of identical hashes within the
// window before ErrLoopDetected is returned.
const DefaultThreshold = 3

// DefaultWindow is the default sliding-window duration.
const DefaultWindow = 60 * time.Second

// ErrLoopDetected is returned by LoopGuard.Observe when the threshold is
// reached for a given hash within the sliding window.
var ErrLoopDetected = errors.New("loopguard: loop detected (threshold exceeded within window)")

// LoopGuard tracks recent tool-call hashes within a sliding time window.
// It is safe for concurrent use.
type LoopGuard struct {
	mu        sync.Mutex
	threshold int
	window    time.Duration
	// buckets maps hash → sorted slice of observation times.
	buckets map[string][]time.Time
}

// New returns a LoopGuard that trips when `threshold` identical hashes are
// observed within `window`.
func New(threshold int, window time.Duration) *LoopGuard {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return &LoopGuard{
		threshold: threshold,
		window:    window,
		buckets:   make(map[string][]time.Time),
	}
}

// Observe records one tool-call hash. It returns ErrLoopDetected if the
// threshold has been reached within the current sliding window.
func (g *LoopGuard) Observe(hash string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-g.window)

	// Append the new observation, then drop any older than the window.
	times := append(g.buckets[hash], now)
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	g.buckets[hash] = kept

	if len(g.buckets[hash]) >= g.threshold {
		return ErrLoopDetected
	}
	return nil
}

// Reset clears all state. Useful between agent sessions.
func (g *LoopGuard) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.buckets = make(map[string][]time.Time)
}

// Stats returns the number of distinct hashes currently being tracked
// (after window pruning). Useful for metrics.
func (g *LoopGuard) Stats() (trackedHashes int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune(time.Now())
	return len(g.buckets)
}

func (g *LoopGuard) prune(now time.Time) {
	cutoff := now.Add(-g.window)
	for h, times := range g.buckets {
		kept := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(g.buckets, h)
		} else {
			g.buckets[h] = kept
		}
	}
}
