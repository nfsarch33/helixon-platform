// Tests for MiniMax provider integration with circuit breaker.
package resilience

import (
	"errors"
	"testing"
	"time"
)

// fakeUpstream simulates a flaky MiniMax-like upstream.
type fakeUpstream struct {
	failuresBeforeOK int
	calls            int
	failures         int
}

func (f *fakeUpstream) Call() error {
	f.calls++
	if f.failures < f.failuresBeforeOK {
		f.failures++
		return errors.New("502 bad gateway")
	}
	return nil
}

// TestMinimaxProvider_BreakerTripsAfterFailures asserts the breaker opens
// when upstream fails repeatedly.
func TestMinimaxProvider_BreakerTripsAfterFailures(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "minimax-provider",
		FailureThreshold:  3,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 1,
	})

	upstream := &fakeUpstream{failuresBeforeOK: 100} // always fail
	wrapped := WrapCall(b, func() error { return upstream.Call() })

	// First 3 calls should fail + trip the breaker.
	for i := 0; i < 3; i++ {
		if err := wrapped(); err == nil {
			t.Fatalf("call %d: expected error", i+1)
		}
	}
	// 4th call should fail-fast (no upstream hit).
	if err := wrapped(); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("4th call: want ErrBreakerOpen, got %v", err)
	}
	if upstream.calls != 3 {
		t.Fatalf("upstream.calls: want 3 (fail-fast after), got %d", upstream.calls)
	}
}

// TestMinimaxProvider_4xxFailFast asserts 4xx does not trip the breaker.
func TestMinimaxProvider_4xxFailFast(t *testing.T) {
	b := NewBreaker(BreakerConfig{
		Name:              "minimax-4xx",
		FailureThreshold:  2,
		OpenTimeout:       10 * time.Millisecond,
		HalfOpenSuccesses: 1,
	})

	calls := 0
	wrapped := WrapCall(b, func() error {
		calls++
		return ErrBadRequest
	})

	for i := 0; i < 10; i++ {
		_ = wrapped()
	}
	if b.IsOpen() {
		t.Fatal("breaker should NOT open for 4xx-only errors")
	}
	if calls != 10 {
		t.Fatalf("4xx should not be short-circuited; want 10 calls, got %d", calls)
	}
}
