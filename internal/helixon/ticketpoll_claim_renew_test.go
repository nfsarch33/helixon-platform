package helixon

import (
	"context"
	"net/http"
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

// v18860-1 renew-409: a 409 from the renew route means the lease was swept
// or taken by another agent. The poller must cancel the run, count
// lease_lost, tell the board via a comment, and never call CompleteTicket —
// reporting would write a ticket this agent no longer owns.

func TestRunTicketRenew409CancelsRunAndNeverCompletes(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "t-lost", Title: "lease lost"})
	board.mu.Lock()
	board.renewStatus = http.StatusConflict
	board.mu.Unlock()
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "poller",
	}, quietLogger())

	canceled := make(chan struct{})
	work := func(ctx context.Context, _ controlplane.Ticket) (string, error) {
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.ClaimRenewInterval = 10 * time.Millisecond
		c.TicketTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "lease loss recorded", func() bool {
		return p.Stats().LeaseLost == 1
	})
	waitFor(t, "run context canceled after the 409 renew", func() bool {
		select {
		case <-canceled:
			return true
		default:
			return false
		}
	})
	waitFor(t, "lease-lost notice posted", func() bool {
		_, _, comments := board.snapshot()
		return len(comments["t-lost"]) > 0
	})
	if s := p.Stats(); s.Completed != 0 {
		t.Fatalf("Completed = %d after lease loss; want 0 (never complete a ticket this agent no longer owns)", s.Completed)
	}
	_, completed, _ := board.snapshot()
	if _, done := completed["t-lost"]; done {
		t.Fatal("CompleteTicket reached the board after a 409 renew")
	}
}

func TestRunTicketRenew404KeepsRunning(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "t-old", Title: "board predates renew"})
	board.mu.Lock()
	board.renewStatus = http.StatusNotFound
	board.mu.Unlock()
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "poller",
	}, quietLogger())

	work := func(ctx context.Context, _ controlplane.Ticket) (string, error) {
		if ctx.Err() != nil {
			t.Error("run context canceled on a best-effort renew failure")
		}
		return "evidence: PASS: 1, FAIL: 0 ok example/pkg 1.0s", nil
	}
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.ClaimRenewInterval = 10 * time.Millisecond
		c.TicketTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "ticket completes despite the 404 renew", func() bool {
		return p.Stats().Completed == 1
	})
	if s := p.Stats(); s.LeaseLost != 0 {
		t.Fatalf("LeaseLost = %d on a 404 renew; want 0 (best-effort, not definitive)", s.LeaseLost)
	}
	_, _, comments := board.snapshot()
	if n := len(comments["t-old"]); n != 0 {
		t.Fatalf("AddComment called %d times on a best-effort path; want 0", n)
	}
}
