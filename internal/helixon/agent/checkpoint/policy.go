// Package checkpoint implements the agent's memory flush policy.
//
// FlushPolicy controls when the agent runtime triggers a memory
// Flush on the configured memory backend. The default policy fires
// every N agent steps (default 5), which bounds the rate of network
// round-trips against the Engram server while keeping the local
// hot cache close to the agent's working set.
//
// Why a policy at all: the Engram HTTP backend writes inline, so
// every agent step would otherwise be a network round-trip. The
// policy collapses bursts of steps into one flush, while still
// guaranteeing that no more than N steps of working memory can be
// lost on a crash.
package checkpoint

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
)

// DefaultEveryN is the default flush cadence. One flush every
// 5 agent steps balances Engram network cost against crash loss.
const DefaultEveryN = 5

// Flusher is the contract the policy needs from a memory backend.
type Flusher interface {
	Flush(ctx context.Context) error
}

// FlushPolicy decides when to call Flusher.Flush.
//
// The zero value is invalid; use New.
type FlushPolicy struct {
	backend Flusher
	everyN  int
	logger  *slog.Logger

	count   atomic.Int64
	flushes atomic.Int64
}

// New returns a policy that flushes every N steps. everyN <= 0
// is replaced with DefaultEveryN.
func New(backend Flusher, everyN int, logger *slog.Logger) *FlushPolicy {
	if everyN <= 0 {
		everyN = DefaultEveryN
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &FlushPolicy{
		backend: backend,
		everyN:  everyN,
		logger:  logger.With(slog.String("component", "helixon.checkpoint.flush_policy")),
	}
}

// OnStep is called after every agent step. It increments the
// internal counter and triggers a flush when the cadence is met.
func (p *FlushPolicy) OnStep(ctx context.Context) error {
	n := p.count.Add(1)
	if int(n)%p.everyN != 0 {
		return nil
	}
	if err := p.backend.Flush(ctx); err != nil {
		p.logger.Warn("flush failed", slog.Int64("step", n), slog.String("err", err.Error()))
		return err
	}
	p.flushes.Add(1)
	p.logger.Debug("flushed", slog.Int64("step", n), slog.Int64("flushes", p.flushes.Load()))
	return nil
}

// ForceNow triggers an immediate flush regardless of cadence.
// Useful at agent shutdown.
func (p *FlushPolicy) ForceNow(ctx context.Context) error {
	if err := p.backend.Flush(ctx); err != nil {
		return err
	}
	p.flushes.Add(1)
	return nil
}

// Stats reports the policy's counters.
func (p *FlushPolicy) Stats() (steps, flushes int64) {
	return p.count.Load(), p.flushes.Load()
}

// ComposeWith returns a Flusher that calls FlushPolicy.ForceNow.
// Useful for wiring the policy into a "deferred at shutdown" path.
func (p *FlushPolicy) ComposeWith() Flusher {
	return flushFunc(func(ctx context.Context) error { return p.ForceNow(ctx) })
}

type flushFunc func(ctx context.Context) error

func (f flushFunc) Flush(ctx context.Context) error { return f(ctx) }

// Compile-time guard: ensure the policy + memory.Backend typecheck.
var _ = memory.NewInMemoryBackend
