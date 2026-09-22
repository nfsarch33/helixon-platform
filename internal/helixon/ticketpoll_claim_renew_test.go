package helixon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
)

// v18860-1: a live run renews its claim lease periodically so the board's
// stale-claim sweeper never releases a ticket out from under work that is
// still progressing — the exact hazard v18855's in-place infra retry creates,
// since a bounded retry chain can hold a claim well past a fixed staleness
// window.

func TestClaimRenewedWhileWorkRuns(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "t-renew", Title: "renewal"})
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "poller",
	}, quietLogger())

	workDone := make(chan struct{})
	work := func(context.Context, controlplane.Ticket) (string, error) {
		<-workDone // hold the claim open until the test lets go
		return "evidence: renewed while running", nil
	}
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.ClaimRenewInterval = 10 * time.Millisecond
		c.TicketTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "at least two lease renewals while the run is alive", func() bool {
		board.mu.Lock()
		defer board.mu.Unlock()
		return len(board.renewals) >= 2
	})
	for _, r := range board.renewals {
		if !strings.HasPrefix(r, "t-renew by ") {
			t.Fatalf("renewal recorded as %q; want the ticket id and the claimant", r)
		}
	}
	if s := p.Stats(); s.ClaimRenewed < 2 {
		t.Fatalf("ClaimRenewed = %d, want >= 2", s.ClaimRenewed)
	}
	close(workDone)
	waitFor(t, "ticket completes after renewal", func() bool {
		return p.Stats().Completed == 1
	})
}

func TestClaimRenewDisabledByNegativeConfig(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "t-norenew", Title: "no renewal"})
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "poller",
	}, quietLogger())
	p := testPoller(t, client, func(context.Context, controlplane.Ticket) (string, error) {
		return "evidence", nil
	}, func(c *TicketPollerConfig) {
		c.ClaimRenewInterval = -1 // disabled, pinned
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "ticket completes without any renewal", func() bool {
		return p.Stats().Completed == 1
	})
	board.mu.Lock()
	defer board.mu.Unlock()
	if len(board.renewals) != 0 || p.Stats().ClaimRenewed != 0 {
		t.Fatalf("renewals=%d claimRenewed=%d, want 0/0 when disabled", len(board.renewals), p.Stats().ClaimRenewed)
	}
}
