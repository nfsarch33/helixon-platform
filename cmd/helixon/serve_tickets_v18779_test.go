package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"

	_ "modernc.org/sqlite"
)

// v18779: an agent that pulls its own work must say so at every start. A
// silent banner is how "answers when spoken to" and "claims and executes
// tickets unattended" become indistinguishable from the console.

func TestTicketPollerBannerNamesTheState(t *testing.T) {
	t.Parallel()

	off := ticketPollerBanner(nil)
	if !strings.Contains(off, "DISABLED") {
		t.Fatalf("off banner = %q, want it to say DISABLED", off)
	}
	if !strings.Contains(off, "tickets.enabled") {
		t.Errorf("off banner does not say how to turn it on: %q", off)
	}

	p, err := helixon.NewTicketPoller(
		helixon.TicketPollerConfig{
			Enabled: true, Interval: 45 * time.Second, MaxConcurrent: 2,
			TicketTimeout: 12 * time.Minute, Status: "ready",
		},
		controlplane.NewSprintboardClient(controlplane.SprintboardConfig{AgentName: "a"}, slog.Default()),
		func(context.Context, controlplane.Ticket) (string, error) { return "", nil },
		"a", 0, slog.Default(),
	)
	if err != nil {
		t.Fatalf("NewTicketPoller: %v", err)
	}
	on := ticketPollerBanner(p)
	for _, want := range []string{"ENABLED", `"ready"`, "45s", "max_concurrent=2", "12m0s"} {
		if !strings.Contains(on, want) {
			t.Errorf("on banner %q is missing %q", on, want)
		}
	}
	if strings.Contains(on, "DISABLED") {
		t.Errorf("an enabled poller must not print DISABLED: %q", on)
	}
}
