package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Tier represents a provider priority level (lower = higher priority).
type Tier int

const (
	// TierFree is the highest priority tier (e.g. Gemini CLI headless via subscription).
	TierFree Tier = 1
	// TierCapped has a monthly spending cap (e.g. Claude Code CLI).
	TierCapped Tier = 2
	// TierPayAsYouGo is metered API usage (e.g. Gemini Flash-Lite API).
	TierPayAsYouGo Tier = 3
	// TierLocal uses locally hosted models (e.g. Qwen via llm-cluster-router).
	TierLocal Tier = 4
)

// ErrAllProvidersFailed is returned when all tiered providers fail.
var ErrAllProvidersFailed = errors.New("all tiered providers failed")

// TieredProvider associates a Provider with its tier and health state.
type TieredProvider struct {
	Name     string
	Tier     Tier
	Provider Provider

	mu          sync.Mutex
	healthy     bool
	failCount   int
	lastFailure time.Time
	cooldown    time.Duration
}

// NewTieredProvider creates a TieredProvider with default health settings.
func NewTieredProvider(name string, tier Tier, provider Provider) *TieredProvider {
	return &TieredProvider{
		Name:     name,
		Tier:     tier,
		Provider: provider,
		healthy:  true,
		cooldown: 30 * time.Second,
	}
}

func (tp *TieredProvider) isAvailable() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if tp.healthy {
		return true
	}
	if time.Since(tp.lastFailure) > tp.cooldown {
		tp.healthy = true
		tp.failCount = 0
		return true
	}
	return false
}

func (tp *TieredProvider) recordSuccess() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.healthy = true
	tp.failCount = 0
}

func (tp *TieredProvider) recordFailure() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.failCount++
	tp.lastFailure = time.Now()
	if tp.failCount >= 3 {
		tp.healthy = false
		tp.cooldown = time.Duration(tp.failCount) * 10 * time.Second
		if tp.cooldown > 5*time.Minute {
			tp.cooldown = 5 * time.Minute
		}
	}
}

// HealthStatus returns the current health info for observability.
func (tp *TieredProvider) HealthStatus() (healthy bool, failCount int) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.healthy, tp.failCount
}

// TieredRouter routes LLM requests through providers in tier order, falling
// back to the next tier on failure. Implements Provider.
type TieredRouter struct {
	providers []*TieredProvider // sorted by tier ascending
}

// NewTieredRouter creates a router from a list of tiered providers.
// Providers are internally sorted by tier (lowest tier number = highest priority).
func NewTieredRouter(providers []*TieredProvider) *TieredRouter {
	sorted := make([]*TieredProvider, len(providers))
	copy(sorted, providers)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Tier < sorted[j-1].Tier; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return &TieredRouter{providers: sorted}
}

// Complete tries each provider in tier order until one succeeds.
func (r *TieredRouter) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	var lastErr error
	for _, tp := range r.providers {
		if !tp.isAvailable() {
			continue
		}

		resp, err := tp.Provider.Complete(ctx, req)
		if err != nil {
			tp.recordFailure()
			lastErr = fmt.Errorf("provider %s (tier %d): %w", tp.Name, tp.Tier, err)
			log.Printf("[router] provider %s failed: %v, trying next tier", tp.Name, err)
			continue
		}

		tp.recordSuccess()
		return resp, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrAllProvidersFailed, lastErr)
	}
	return nil, ErrAllProvidersFailed
}

// Providers returns the list of configured providers for inspection.
func (r *TieredRouter) Providers() []*TieredProvider {
	return r.providers
}
