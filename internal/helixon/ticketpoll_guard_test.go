// v18857, §3.2 Q2a: the test-file write guard must be scoped to ticket
// runs. These tests pin the poller side — the guard is held for exactly
// the life of runTicket — using the same stub-board harness as
// ticketpoll_test.go.
package helixon

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

// guardBoard serves one ticket on the first search, then nothing.
type guardBoard struct {
	served bool
}

func (b *guardBoard) SearchTickets(context.Context, controlplane.TicketFilter) ([]controlplane.Ticket, error) {
	if b.served {
		return nil, nil
	}
	b.served = true
	return []controlplane.Ticket{{ID: "t-guard-1", Status: "ready"}}, nil
}

func (b *guardBoard) ClaimTicket(context.Context, string) error            { return nil }
func (b *guardBoard) CompleteTicket(context.Context, string, string) error { return nil }
func (b *guardBoard) AddComment(context.Context, string, string, string) error {
	return nil
}

// TestRunTicketHoldsTestFileGuardForTicketLife proves the activation
// window: active inside work, released once the run reports.
func TestRunTicketHoldsTestFileGuardForTicketLife(t *testing.T) {
	guard := &tooldispatch.TestFileGuard{}

	activeDuringWork := false
	work := func(ctx context.Context, ticket controlplane.Ticket) (string, error) {
		activeDuringWork = guard.Active()
		return "evidence: guard-window observed", nil
	}

	p, err := NewTicketPoller(
		TicketPollerConfig{Enabled: true, Status: "ready", Interval: time.Hour, MaxConcurrent: 1, TicketTimeout: time.Minute},
		&guardBoard{},
		work,
		"agent-test",
		0,
		slog.Default(),
		WithTestFileGuard(guard),
	)
	if err != nil {
		t.Fatalf("NewTicketPoller: %v", err)
	}

	p.runTicket(context.Background(), controlplane.Ticket{ID: "t-guard-1"})

	if !activeDuringWork {
		t.Fatal("guard inactive during ticket work")
	}
	if guard.Active() {
		t.Fatal("guard still active after the ticket run finished")
	}
}

// TestRunTicketReleasesGuardOnWorkError proves a failing run cannot leak
// the activation: the next human session would silently inherit the
// write restriction.
func TestRunTicketReleasesGuardOnWorkError(t *testing.T) {
	guard := &tooldispatch.TestFileGuard{}
	work := func(ctx context.Context, ticket controlplane.Ticket) (string, error) {
		return "", context.DeadlineExceeded
	}
	p, err := NewTicketPoller(
		TicketPollerConfig{Enabled: true, Status: "ready", Interval: time.Hour, MaxConcurrent: 1, TicketTimeout: time.Minute},
		&guardBoard{},
		work,
		"agent-test",
		0,
		slog.Default(),
		WithTestFileGuard(guard),
	)
	if err != nil {
		t.Fatalf("NewTicketPoller: %v", err)
	}

	p.runTicket(context.Background(), controlplane.Ticket{ID: "t-guard-1"})

	if guard.Active() {
		t.Fatal("guard leaked after a failed ticket run")
	}
}

// TestNewTicketPollerNilGuardStillWorks pins that the option is optional:
// runtimes without the guard (tests, non-file-tool agents) are unaffected.
func TestNewTicketPollerNilGuardStillWorks(t *testing.T) {
	p, err := NewTicketPoller(
		TicketPollerConfig{Enabled: true, Status: "ready", Interval: time.Hour, TicketTimeout: time.Minute},
		&guardBoard{},
		func(context.Context, controlplane.Ticket) (string, error) { return "ok", nil },
		"agent-test",
		0,
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("NewTicketPoller without guard: %v", err)
	}
	if p.testGuard != nil {
		t.Fatal("testGuard set without the option")
	}
}
