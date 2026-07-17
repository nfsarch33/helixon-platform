package evolver

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultMaxRetries is the retry budget per Source.Observe call.
const DefaultMaxRetries = 3

// retryBackoff is the inter-attempt delay. Multiplied by attempt number.
const retryBackoff = 100 * time.Millisecond

// Evolver orchestrates one cycle: Source → Distill → Promote.
type Evolver struct {
	source   Source
	distill  Distill
	promote  Promote
	maxRetry int
	dedupe   map[string]bool
}

// NewEvolver wires the three interfaces. Returns ErrNoSubsystems if
// any interface is nil.
func NewEvolver(s Source, d Distill, p Promote) (*Evolver, error) {
	if s == nil || d == nil || p == nil {
		return nil, ErrNoSubsystems
	}
	return &Evolver{
		source:   s,
		distill:  d,
		promote:  p,
		maxRetry: DefaultMaxRetries,
		dedupe:   map[string]bool{},
	}, nil
}

// SetMaxRetries overrides the default retry budget. Pass 0 to disable
// retries entirely.
func (e *Evolver) SetMaxRetries(n int) {
	e.maxRetry = n
}

// CycleResult summarizes one Cycle invocation.
type CycleResult struct {
	Observed     int           // Observations pulled from the Source
	Reflected    int           // Candidates produced by the Distill
	Promoted     int           // Candidates successfully persisted
	Skipped      int           // Candidates dropped because of dedupe
	Duration     time.Duration // wall-clock of the cycle
	ObserveError error         // last Observe error (nil on success)
}

// Cycle runs one full Source → Distill → Promote round. On Source
// failure the cycle is aborted (no Candidates generated); on Promote
// failure the Candidate is reported as a skip but the cycle continues
// with the next Candidate.
func (e *Evolver) Cycle(ctx context.Context) (*CycleResult, error) {
	start := time.Now()
	res := &CycleResult{}

	// 1. Source
	obs, err := e.observeWithRetry(ctx)
	if err != nil {
		res.ObserveError = err
		res.Duration = time.Since(start)
		return res, fmt.Errorf("evolver: source %s failed: %w", e.source.Name(), err)
	}
	res.Observed = len(obs)
	if len(obs) == 0 {
		res.Duration = time.Since(start)
		return res, nil
	}

	// 2. Distill
	cands, err := e.distill.Reflect(ctx, obs)
	if err != nil {
		res.Duration = time.Since(start)
		return res, fmt.Errorf("evolver: distill failed: %w", err)
	}
	res.Reflected = len(cands)

	// 3. Promote (with per-Candidate error tolerance)
	for _, c := range cands {
		if e.dedupe[c.ID] {
			res.Skipped++
			continue
		}
		if err := e.promote.Persist(ctx, c); err != nil {
			res.Skipped++
			continue
		}
		e.dedupe[c.ID] = true
		res.Promoted++
	}

	res.Duration = time.Since(start)
	return res, nil
}

func (e *Evolver) observeWithRetry(ctx context.Context) ([]Observation, error) {
	var lastErr error
	for attempt := 0; attempt <= e.maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryBackoff * time.Duration(attempt)):
			}
		}
		obs, err := e.source.Observe(ctx)
		if err == nil {
			return obs, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("observe failed after %d attempts: %w", e.maxRetry+1, lastErr)
}
