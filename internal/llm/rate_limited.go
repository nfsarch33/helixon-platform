package llm

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RateLimitConfig controls rate limiting behavior.
type RateLimitConfig struct {
	RequestsPerMinute int           // max requests per sliding window
	Window            time.Duration // window size; defaults to 1 minute
	BurstSize         int           // allow short bursts above steady rate; 0 = no burst
}

// DefaultRateLimitConfig returns conservative defaults suitable for CLI-based providers.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		RequestsPerMinute: 10,
		Window:            time.Minute,
		BurstSize:         2,
	}
}

// RateLimitedProvider wraps a Provider with a sliding-window rate limiter.
// It implements Provider and blocks (with context awareness) when the limit is hit.
type RateLimitedProvider struct {
	inner  Provider
	name   string
	config RateLimitConfig

	mu      sync.Mutex
	tokens  []time.Time // sliding window of request timestamps
	nowFunc func() time.Time
}

// NewRateLimitedProvider wraps a provider with rate limiting.
func NewRateLimitedProvider(name string, inner Provider, cfg RateLimitConfig) *RateLimitedProvider {
	if cfg.Window == 0 {
		cfg.Window = time.Minute
	}
	maxTokens := cfg.RequestsPerMinute + cfg.BurstSize
	return &RateLimitedProvider{
		inner:   inner,
		name:    name,
		config:  cfg,
		tokens:  make([]time.Time, 0, maxTokens),
		nowFunc: time.Now,
	}
}

// Complete rate-limits and delegates to the inner provider.
func (r *RateLimitedProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if err := r.waitForSlot(ctx); err != nil {
		return nil, fmt.Errorf("rate limit for %s: %w", r.name, err)
	}
	return r.inner.Complete(ctx, req)
}

func (r *RateLimitedProvider) waitForSlot(ctx context.Context) error {
	maxAllowed := r.config.RequestsPerMinute + r.config.BurstSize
	for {
		r.mu.Lock()
		now := r.nowFunc()
		cutoff := now.Add(-r.config.Window)

		// Prune expired timestamps
		valid := 0
		for _, t := range r.tokens {
			if t.After(cutoff) {
				r.tokens[valid] = t
				valid++
			}
		}
		r.tokens = r.tokens[:valid]

		if len(r.tokens) < maxAllowed {
			r.tokens = append(r.tokens, now)
			r.mu.Unlock()
			return nil
		}

		earliest := r.tokens[0]
		waitUntil := earliest.Add(r.config.Window)
		waitDur := waitUntil.Sub(now)
		r.mu.Unlock()

		if waitDur <= 0 {
			continue
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: waiting for rate limit slot", ctx.Err())
		case <-time.After(waitDur):
		}
	}
}

// Stats returns current rate limiter state for observability.
func (r *RateLimitedProvider) Stats() (activeTokens int, maxAllowed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.nowFunc()
	cutoff := now.Add(-r.config.Window)
	count := 0
	for _, t := range r.tokens {
		if t.After(cutoff) {
			count++
		}
	}
	return count, r.config.RequestsPerMinute + r.config.BurstSize
}
