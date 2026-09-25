package helixon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
)

// When the agent's own run-store lease is lost or lapses under it, the run
// is resumable and the recovery sweep will finish it and report through
// ReportRecovered. Escalating the ticket here would strand work that is
// still alive - the
func TestRunTicketAgentLeaseLostAbandonsWithoutEscalating(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "t-lease", Title: "run lease lost"})
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "poller",
	}, quietLogger())

	work := func(context.Context, controlplane.Ticket) (string, error) {
		return "", fmt.Errorf("run lease lost: %w", agent.ErrLeaseLost)
	}
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.TicketTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "ticket abandoned on agent lease loss", func() bool {
		return p.Stats().Abandoned == 1
	})
	s := p.Stats()
	if s.Escalated != 0 {
		t.Fatalf("Escalated = %d on a resumable lease loss; want 0 (the recovery sweep owns the report)", s.Escalated)
	}
	if s.Completed != 0 {
		t.Fatalf("Completed = %d; want 0", s.Completed)
	}
	_, completed, comments := board.snapshot()
	if _, done := completed["t-lease"]; done {
		t.Fatal("CompleteTicket reached the board for a resumable run")
	}
	if n := len(comments["t-lease"]); n != 0 {
		t.Fatalf("escalation comment posted %d times for a resumable run; want 0", n)
	}
}
