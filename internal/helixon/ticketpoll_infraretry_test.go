package helixon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// v18855: infra-class LLM failures (provider 429/5xx) are retried under the
// existing claim, bounded by MaxInfraRetries, before any escalation. These
// tests pin the three behaviours the board census demanded: a transient
// outage completes instead of escalating; a persistent outage escalates with
// the retry budget visible in the cause; a caller-fault error never retries.

func apiErr(code int) error {
	return &llm.APIError{StatusCode: code, Body: "upstream unavailable"}
}

func TestIsInfraFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"502", apiErr(502), true},
		{"503", apiErr(503), true},
		{"500", apiErr(500), true},
		{"429", apiErr(429), true},
		{"404 wrong model", apiErr(404), false},
		{"401 bad key", apiErr(401), false},
		{"400 bad request", apiErr(400), false},
		{"plain error", errors.New("boom"), false},
		{"wrapped 502", fmt.Errorf("iter 2: %w", apiErr(502)), true},
		{"wrapped 400", fmt.Errorf("iter 2: %w", apiErr(400)), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := llm.IsInfraFailure(tc.err); got != tc.want {
				t.Fatalf("IsInfraFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func runInfraRetryPoller(t *testing.T, work TicketWorker) (*fakeBoard, *TicketPoller) {
	t.Helper()
	board := newFakeBoard(controlplane.Ticket{ID: "t-infra", Title: "infra retry"})
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{BaseURL: srv.URL}, quietLogger())
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.TicketTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return board, p
}

func TestInfraFailureRetriedUnderClaimThenCompletes(t *testing.T) {
	calls := 0
	work := func(context.Context, controlplane.Ticket) (string, error) {
		calls++
		if calls == 1 {
			return "", apiErr(502) // the exact census failure: status 502
		}
		return "evidence: recovered after one retry", nil
	}
	board, p := runInfraRetryPoller(t, work)

	waitFor(t, "ticket completed after one in-place retry", func() bool {
		s := p.Stats()
		return s.Completed == 1 && s.InfraRetried == 1 && s.Escalated == 0
	})
	claims, completed, _ := board.snapshot()
	if len(claims) != 1 || claims[0] != "t-infra" {
		t.Fatalf("the retry must happen under the SAME claim, claims = %v", claims)
	}
	if completed["t-infra"] == "" {
		t.Fatal("ticket must be completed with evidence")
	}
	if calls != 2 {
		t.Fatalf("work ran %d times, want 2 (one failure + one success)", calls)
	}
}

func TestInfraFailureEscalatesAfterBudgetWithRetryCountInCause(t *testing.T) {
	work := func(context.Context, controlplane.Ticket) (string, error) {
		return "", apiErr(502)
	}
	board, p := runInfraRetryPoller(t, work)

	// Escalated is counted BEFORE the comment is posted, so the comment is
	// the reliable completion signal here.
	waitFor(t, "escalation comment after exhausting the retry budget", func() bool {
		_, _, comments := board.snapshot()
		return len(comments["t-infra"]) == 1
	})
	s := p.Stats()
	if s.Escalated != 1 || s.InfraRetried != 2 {
		t.Fatalf("escalated=%d infraRetried=%d, want 1/2", s.Escalated, s.InfraRetried)
	}
	_, _, comments := board.snapshot()
	body := comments["t-infra"][0]
	if !strings.Contains(body, "persisted after 2 retries") || !strings.Contains(body, "status 502") {
		t.Fatalf("escalation cause must name the budget and the underlying status, got: %s", body)
	}
}

func TestCallerFaultErrorNeverRetries(t *testing.T) {
	calls := 0
	work := func(context.Context, controlplane.Ticket) (string, error) {
		calls++
		return "", apiErr(404) // wrong model name: escalation-class immediately
	}
	board, p := runInfraRetryPoller(t, work)

	// Escalated is counted BEFORE the comment is posted (same ordering as
	// the budget test), so the comment is the completion signal.
	waitFor(t, "caller-fault error escalates immediately", func() bool {
		_, _, comments := board.snapshot()
		return len(comments["t-infra"]) == 1
	})
	s := p.Stats()
	if s.Escalated != 1 || s.InfraRetried != 0 {
		t.Fatalf("escalated=%d infraRetried=%d, want 1/0", s.Escalated, s.InfraRetried)
	}
	if calls != 1 {
		t.Fatalf("work ran %d times, want 1 (no retries for caller-fault)", calls)
	}
	_, _, comments := board.snapshot()
	if strings.Contains(comments["t-infra"][0], "persisted after") {
		t.Fatalf("escalation must be the plain error, comments = %v", comments["t-infra"])
	}
}

func TestInfraRetryDisabledByNegativeConfig(t *testing.T) {
	calls := 0
	work := func(context.Context, controlplane.Ticket) (string, error) {
		calls++
		return "", apiErr(502)
	}
	board := newFakeBoard(controlplane.Ticket{ID: "t-off", Title: "retry off"})
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{BaseURL: srv.URL}, quietLogger())
	p := testPoller(t, client, work, func(c *TicketPollerConfig) {
		c.MaxInfraRetries = -1 // pre-v18855 behaviour, pinned
		c.TicketTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "escalates on the first infra failure when disabled", func() bool {
		return p.Stats().Escalated == 1
	})
	if calls != 1 || p.Stats().InfraRetried != 0 {
		t.Fatalf("disabled retry must not retry: calls=%d infraRetried=%d", calls, p.Stats().InfraRetried)
	}
}
