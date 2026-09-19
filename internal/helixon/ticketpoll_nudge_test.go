package helixon

import (
	"context"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"sync/atomic"
	"testing"
	"time"
)

// v18851 Nudge: the human-in-the-loop tight-loop primitive. When an operator
// requeues or creates a ticket from the console, the poller would otherwise
// sit in its idle backoff (doubling to 5 minutes) before noticing. Nudge
// wakes the wait immediately and resets the backoff, so the board's own GUI
// is also the agent's launch button.
func TestTicketPoller_NudgeWakesIdleBackoffImmediately(t *testing.T) {
	var n atomic.Int64
	board := &nudgeBoard{searches: &n}

	cfg := TicketPollerConfig{
		Enabled: true, Status: "ready",
		Interval: 30 * time.Second, MaxBackoff: 5 * time.Minute, MaxConcurrent: 1, TicketTimeout: 2 * time.Hour,
	}
	p, err := NewTicketPoller(cfg, board, func(context.Context, controlplane.Ticket) (string, error) { return "ok", nil }, "nudge-test", time.Hour,
		quietLogger())
	if err != nil {
		t.Fatalf("NewTicketPoller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()

	// Wait for the first (immediate) poll, then let it go idle: the second
	// poll is 30s away at the earliest without a nudge.
	waitFor(t, "first poll", func() bool { return n.Load() >= 1 })
	time.Sleep(150 * time.Millisecond) // let the loop enter its timer wait

	p.Nudge()

	// The nudge must produce a second search within a second — far inside
	// the 30s interval the loop was waiting on.
	waitFor(t, "poll after nudge", func() bool { return n.Load() >= 2 })
	if got := n.Load(); got < 2 {
		t.Fatalf("searches after nudge = %d, want >= 2 (idle backoff was not woken)", got)
	}
}

type nudgeBoard struct{ searches *atomic.Int64 }

func (b *nudgeBoard) SearchTickets(context.Context, controlplane.TicketFilter) ([]controlplane.Ticket, error) {
	b.searches.Add(1)
	return nil, nil
}
func (b *nudgeBoard) ClaimTicket(context.Context, string) error            { return nil }
func (b *nudgeBoard) CompleteTicket(context.Context, string, string) error { return nil }
func (b *nudgeBoard) AddComment(context.Context, string, string, string) error {
	return nil
}

type noopWorker struct{}

func (noopWorker) Run(context.Context, string) (string, error) { return "ok", nil }
