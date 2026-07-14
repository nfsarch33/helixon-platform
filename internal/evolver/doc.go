// Package evolver is the generic self-improvement engine for the
// Helixon platform (ADR-020). It exposes three small interfaces that
// any evolving subsystem (Agentrace, eval matrix, LLM router,
// notification policy) can implement to participate in ORHEP cycles.
//
// The package ships with a Cycle driver that:
//
//  1. Pulls Observations from the registered Source.
//  2. Reflects them into Candidates via Distill.
//  3. Persists valid Candidates via Promote.
//
// Concrete subsystems keep their own state; this package only
// orchestrates. Storage of durable artifacts (DRL policy files, eval
// matrix updates, failover rules) is the Promote implementation's
// responsibility — the Evolver never writes business state directly.
package evolver

import (
	"context"
	"errors"
)

// Observation is one data point a Source emits per cycle.
type Observation struct {
	// Source is the registered Source name (e.g. "agentrace",
	// "eval-matrix", "llm-router").
	Source string
	// Key dedupes Observations across cycles. Two Observations with
	// the same Key within the same Source are treated as identical.
	Key string
	// Payload is the opaque observation body. The Distill
	// implementation decides how to interpret it.
	Payload any
}

// Candidate is a proposed mutation produced by a Distill.
type Candidate struct {
	// ID uniquely identifies the Candidate. The Evolver dedupes
	// across cycles by ID, so it should be stable (e.g. content hash).
	ID string
	// Source is the Source that produced the Observation this
	// Candidate was distilled from.
	Source string
	// Title is a short human-readable label for log lines.
	Title string
	// Mutated is the proposed change. The Promote implementation
	// decides how to persist it.
	Mutated any
}

// Source produces Observations.
type Source interface {
	// Name returns the registered Source identifier (used as
	// Observation.Source).
	Name() string
	// Observe fetches Observations since the last successful cycle.
	// A transient error must be returned so the driver can retry.
	Observe(ctx context.Context) ([]Observation, error)
}

// Distill reflects Observations into one or more Candidates.
type Distill interface {
	// Reflect MUST be pure: same Observations → same Candidates.
	// Returning an empty slice is a valid no-op cycle.
	Reflect(ctx context.Context, in []Observation) ([]Candidate, error)
}

// Promote persists a Candidate durably. The Promote implementation
// owns the storage backend (DRL markdown, eval YAML, etc.).
type Promote interface {
	// Persist MUST be idempotent on Candidate.ID; the driver dedupes
	// but Promote is allowed to re-attempt.
	Persist(ctx context.Context, c Candidate) error
}

// ErrNoSubsystems is returned by NewEvolver when at least one of
// Source / Distill / Promote is nil.
var ErrNoSubsystems = errors.New("evolver: Source, Distill, and Promote are all required")
