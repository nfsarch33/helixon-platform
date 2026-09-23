package helixon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
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
	// The worker selects on ctx.Done too, and workDone closes from
	// t.Cleanup: an assertion that fails mid-test must not leave a worker
	// blocked forever inside a Run the cleanup then waits on (the shape
	// that hung this suite to its package deadline once).
	work := func(ctx context.Context, _ controlplane.Ticket) (string, error) {
		select {
		case <-workDone:
			return "evidence: renewed while running", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	t.Cleanup(func() {
		select {
		case <-workDone:
			// already closed mid-test
		default:
			close(workDone)
		}
	})
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.ClaimRenewInterval = 10 * time.Millisecond
		c.TicketTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	// Wait on the STAT, not the board's renewal list: the stat is bumped
	// under the poller's lock after each successful renew, and asserting on
	// the list mid-flight is what let a fatal skip the workDone close.
	waitFor(t, "at least two lease renewals while the run is alive", func() bool {
		return p.Stats().ClaimRenewed >= 2
	})
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

// A 409 from the renew route means the lease was swept or taken by another
// agent. The poller must cancel the run, count lease_lost, tell the board
// via a comment, and never call CompleteTicket or escalate — reporting
// would write a ticket this agent no longer owns. The worker below returns
// SUCCESS after the cancel on purpose: a worker that fails on its own
// masks a deleted discard guard, and a TicketTimeout equal to the waitFor
// budget masks a deleted cancel.

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
	var leaseCause atomic.Value
	work := func(ctx context.Context, _ controlplane.Ticket) (string, error) {
		<-ctx.Done()
		if c := context.Cause(ctx); c != nil {
			leaseCause.Store(c)
		}
		close(canceled)
		// SUCCESS evidence: if the discard guard is deleted, this result is
		// reported and Completed becomes 1 - the test fails.
		return "evidence: PASS: 1, FAIL: 0 ok example/pkg 1.0s", nil
	}
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.ClaimRenewInterval = 10 * time.Millisecond
		c.TicketTimeout = 60 * time.Second // far above waitFor's 5s budget
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "lease loss recorded", func() bool { return p.Stats().LeaseLost == 1 })
	waitFor(t, "run context canceled by the lease loss (not by any deadline)", func() bool {
		select {
		case <-canceled:
			return true
		default:
			return false
		}
	})
	waitFor(t, "exactly one lease-lost notice posted", func() bool {
		_, _, comments := board.snapshot()
		return len(comments["t-lost"]) == 1 && strings.Contains(comments["t-lost"][0], "lease")
	})
	// Wait for the run to be OVER before asserting on outcomes: the notice
	// can land while a deleted discard guard still reports the (successful)
	// result, and asserting early would let that mutant pass. The reserved
	// set is released only after runTicket returns.
	waitFor(t, "ticket run finished (released from the reserved set)", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		_, held := p.reserved["t-lost"]
		return !held
	})
	s := p.Stats()
	if s.Completed != 0 {
		t.Fatalf("Completed = %d after lease loss; want 0 (never complete a ticket this agent no longer owns)", s.Completed)
	}
	if got, _ := leaseCause.Load().(error); !errors.Is(got, controlplane.ErrTicketNotClaimedBy) {
		t.Fatalf("run context cause = %v, want the lease-lost sentinel", got)
	}
	if s.Escalated != 0 {
		t.Fatalf("Escalated = %d after lease loss; want 0 (the ticket is not this agent's to escalate)", s.Escalated)
	}
	if s.Abandoned != 0 {
		t.Fatalf("Abandoned = %d after lease loss; want 0 (counted as LeaseLost only)", s.Abandoned)
	}
	_, completed, _ := board.snapshot()
	if _, done := completed["t-lost"]; done {
		t.Fatal("CompleteTicket reached the board after a 409 renew")
	}
}

// A board that predates the renew route answers 404: best-effort, logged
// and swallowed. The worker holds the claim open until at least one
// renewal actually happened, so the run cannot complete before renewal
// has been exercised.
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
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			board.mu.Lock()
			n := len(board.renewals)
			board.mu.Unlock()
			if n >= 1 {
				return "evidence: PASS: 1, FAIL: 0 ok example/pkg 1.0s", nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Millisecond):
			}
		}
		return "", errors.New("no renewal reached the board within the test budget")
	}
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.ClaimRenewInterval = 10 * time.Millisecond
		c.TicketTimeout = 10 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "ticket completes despite the 404 renew", func() bool {
		return p.Stats().Completed == 1
	})
	s := p.Stats()
	if s.LeaseLost != 0 {
		t.Fatalf("LeaseLost = %d on a 404 renew; want 0 (best-effort, not definitive)", s.LeaseLost)
	}
	if s.Escalated != 0 {
		t.Fatalf("Escalated = %d on a 404 renew; want 0", s.Escalated)
	}
	_, _, comments := board.snapshot()
	if n := len(comments["t-old"]); n != 0 {
		t.Fatalf("AddComment called %d times on a best-effort path; want 0", n)
	}
}
