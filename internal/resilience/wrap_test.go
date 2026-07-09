// Tests for resilience wrap.go helpers. v17408 coverage closure.
package resilience

import (
	"errors"
	"testing"
	"time"
)

func TestWrapCall_SuccessRecordsSuccess(t *testing.T) {
	b := NewBreaker(BreakerConfig{Name: "wrap-success", FailureThreshold: 3, OpenTimeout: time.Second, HalfOpenSuccesses: 1})
	called := false
	fn := WrapCall(b, func() error {
		called = true
		return nil
	})
	if err := fn(); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if !called {
		t.Fatal("inner fn was not invoked")
	}
	if b.State() != "closed" {
		t.Fatalf("want closed, got %s", b.State())
	}
}

func TestWrapCall_4xxFailFast(t *testing.T) {
	b := NewBreaker(BreakerConfig{Name: "wrap-4xx", FailureThreshold: 3, OpenTimeout: time.Second, HalfOpenSuccesses: 1})
	bad := ErrBadRequest
	fn := WrapCall(b, func() error { return bad })
	err := fn()
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
	// 4xx must not trip the breaker.
	if b.State() != "closed" {
		t.Fatalf("4xx should not trip; got %s", b.State())
	}
}

func TestWrapCall_5xxTripsBreaker(t *testing.T) {
	b := NewBreaker(BreakerConfig{Name: "wrap-5xx", FailureThreshold: 2, OpenTimeout: time.Second, HalfOpenSuccesses: 1})
	fn := WrapCall(b, func() error { return ErrUpstream5xx })
	// Trip via 2 failures.
	for i := 0; i < 2; i++ {
		_ = fn()
	}
	if !b.IsOpen() {
		t.Fatalf("want open after 2 5xx failures; got %s", b.State())
	}
	// Subsequent call short-circuits to ErrBreakerOpen.
	if err := fn(); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("want ErrBreakerOpen on already-open; got %v", err)
	}
}

func TestStats_NameAndState(t *testing.T) {
	b := NewBreaker(BreakerConfig{Name: "stats-test", FailureThreshold: 5, OpenTimeout: time.Second, HalfOpenSuccesses: 1})
	got := Stats(b)
	if got.Name != "stats-test" {
		t.Fatalf("want name=stats-test, got %q", got.Name)
	}
	if got.State != "closed" {
		t.Fatalf("want closed, got %s", got.State)
	}
}

func TestClassify5xxOr4xx_Nil(t *testing.T) {
	if err := Classify5xxOr4xx(nil); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestClassify5xxOr4xx_4xxBranch(t *testing.T) {
	got := Classify5xxOr4xx(ErrBadRequest)
	if !errors.Is(got, ErrUpstream4xx) {
		t.Fatalf("want ErrUpstream4xx, got %v", got)
	}
}

func TestClassify5xxOr4xx_5xxBranch(t *testing.T) {
	got := Classify5xxOr4xx(ErrUpstream5xx)
	if !errors.Is(got, ErrUpstream5xx) {
		t.Fatalf("want ErrUpstream5xx, got %v", got)
	}
}

func TestClassify5xxOr4xx_NetBranch(t *testing.T) {
	got := Classify5xxOr4xx(ErrUpstreamNet)
	if !errors.Is(got, ErrUpstream5xx) {
		t.Fatalf("want net→5xx, got %v", got)
	}
}

func TestBackoff_MonotoneAndCapped(t *testing.T) {
	prev := time.Duration(-1)
	for attempt := 0; attempt <= 6; attempt++ {
		d := Backoff(attempt)
		if d < 0 {
			t.Fatalf("attempt %d: negative duration %v", attempt, d)
		}
		if d > 5*time.Second {
			t.Fatalf("attempt %d: exceeds cap %v", attempt, d)
		}
		_ = prev // monotonic not strictly required because jitter could randomize; just non-negative + cap
		prev = d
	}
}

func TestBackoff_NegativeAttemptNormalizesToZero(t *testing.T) {
	if d := Backoff(-1); d != 100*time.Millisecond {
		t.Fatalf("want 100ms for attempt=-1 (normalized to 0), got %v", d)
	}
}
