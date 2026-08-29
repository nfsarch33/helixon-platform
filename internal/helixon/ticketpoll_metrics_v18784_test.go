package helixon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
)

// seriesOf flattens the registry to "name{labels}" -> value, so an assertion
// names the exact series a scrape would see rather than a collector handle the
// exposition might never reach.
func seriesOf(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := map[string]float64{}
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			out[mf.GetName()+labelSuffix(m)] = metricValue(m)
		}
	}
	return out
}

func labelSuffix(m *dto.Metric) string {
	if len(m.GetLabel()) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func metricValue(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	case m.GetHistogram() != nil:
		return float64(m.GetHistogram().GetSampleCount())
	default:
		return 0
	}
}

func meteredPoller(t *testing.T, board TicketBoard, work TicketWorker) (*TicketPoller, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := agentmetrics.New(reg, "poller-test")
	if err != nil {
		t.Fatalf("agentmetrics.New: %v", err)
	}
	cfg := TicketPollerConfig{
		Enabled:       true,
		Interval:      time.Millisecond,
		MaxBackoff:    5 * time.Millisecond,
		MaxConcurrent: 1,
		TicketTimeout: 2 * time.Second,
	}
	p, err := NewTicketPoller(cfg, board, work, "poller", 0, quietLogger(), WithPollerMetrics(m))
	if err != nil {
		t.Fatalf("NewTicketPoller: %v", err)
	}
	return p, reg
}

func boardClient(t *testing.T, b *fakeBoard) *controlplane.SprintboardClient {
	t.Helper()
	srv := b.server(t)
	return controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "poller",
	}, quietLogger())
}

func TestPollerCountsAClaimAndACompletion(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "T-1", Title: "do it", Status: "ready"})
	client := boardClient(t, board)
	p, reg := meteredPoller(t, client, func(_ context.Context, _ controlplane.Ticket) (string, error) {
		return "done, verifier green", nil
	})

	runPoller(t, p, func() bool { return p.Stats().Completed >= 1 })

	got := seriesOf(t, reg)
	if got[agentmetrics.NameTicketsClaimed] != 1 {
		t.Errorf("%s = %v, want 1", agentmetrics.NameTicketsClaimed, got[agentmetrics.NameTicketsClaimed])
	}
	if got[agentmetrics.NameTicketsCompleted] != 1 {
		t.Errorf("%s = %v, want 1", agentmetrics.NameTicketsCompleted, got[agentmetrics.NameTicketsCompleted])
	}
	key := agentmetrics.NameRunDuration + "{outcome=" + agentmetrics.RunCompleted + "}"
	if got[key] != 1 {
		t.Errorf("%s sample count = %v, want 1", key, got[key])
	}
	if got[agentmetrics.NameRunDuration+"{outcome="+agentmetrics.RunEscalated+"}"] != 0 {
		t.Error("a completed run was also filed as escalated")
	}
}

// TestPollerCountsEscalationsByReason is the load-bearing one. Escalation is
// the safety property the reliability tier rests on, and until this counter
// existed an escalation only mutated board state: nothing anywhere told a
// human.
func TestPollerCountsEscalationsByReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		workErr error
		want    string
	}{
		{"verifier failed repeatedly", fmt.Errorf("agent run: %w", agent.ErrNeedsHumanApproval), agentmetrics.ReasonVerifierFailed},
		{"no verifier evidence", fmt.Errorf("agent run: %w", agent.ErrNoVerifierEvidence), agentmetrics.ReasonVerifierFailed},
		{"budget exhausted", fmt.Errorf("agent run: %w", agent.ErrBudgetExhaust), agentmetrics.ReasonBudgetExhausted},
		{"anything else", errors.New("the provider hung up"), agentmetrics.ReasonRunError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			board := newFakeBoard(controlplane.Ticket{ID: "T-1", Title: "do it", Status: "ready"})
			client := boardClient(t, board)
			p, reg := meteredPoller(t, client, func(_ context.Context, _ controlplane.Ticket) (string, error) {
				return "partial output", tc.workErr
			})

			runPoller(t, p, func() bool { return p.Stats().Escalated >= 1 })

			got := seriesOf(t, reg)
			key := agentmetrics.NameEscalations + "{reason=" + tc.want + "}"
			if got[key] != 1 {
				t.Errorf("%s = %v, want 1 (series: %v)", key, got[key], got)
			}
			if got[agentmetrics.NameTicketsCompleted] != 0 {
				t.Error("an escalated ticket was also counted as completed")
			}
			if got[agentmetrics.NameRunDuration+"{outcome="+agentmetrics.RunEscalated+"}"] != 1 {
				t.Error("an escalated run was not timed")
			}
		})
	}
}

// TestEmptyOutputEscalatesAndIsCounted: an empty final message is not evidence,
// and the counter must agree with the board.
func TestEmptyOutputEscalatesAndIsCounted(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "T-1", Title: "do it", Status: "ready"})
	client := boardClient(t, board)
	p, reg := meteredPoller(t, client, func(_ context.Context, _ controlplane.Ticket) (string, error) {
		return "   ", nil
	})

	runPoller(t, p, func() bool { return p.Stats().Escalated >= 1 })

	got := seriesOf(t, reg)
	if got[agentmetrics.NameEscalations+"{reason="+agentmetrics.ReasonRunError+"}"] != 1 {
		t.Errorf("empty output did not count as an escalation: %v", got)
	}
	if got[agentmetrics.NameTicketsCompleted] != 0 {
		t.Error("a ticket with no evidence was counted as completed")
	}
}

// TestPollerWithoutMetricsRecordsNothing is the positive control for every
// assertion above.
//
// Remove WithPollerMetrics from buildTicketPoller and the counters must stay at
// zero. Without this, a test suite could stay green against series that some
// other part of the process happened to move.
func TestPollerWithoutMetricsRecordsNothing(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := agentmetrics.New(reg, "control"); err != nil {
		t.Fatalf("agentmetrics.New: %v", err)
	}
	board := newFakeBoard(controlplane.Ticket{ID: "T-1", Title: "do it", Status: "ready"})
	client := boardClient(t, board)
	// Built WITHOUT WithPollerMetrics: the same work, the same board, no wiring.
	p := testPoller(t, client, func(_ context.Context, _ controlplane.Ticket) (string, error) {
		return "done", nil
	}, nil)

	runPoller(t, p, func() bool { return p.Stats().Completed >= 1 })

	got := seriesOf(t, reg)
	for _, key := range []string{
		agentmetrics.NameTicketsClaimed,
		agentmetrics.NameTicketsCompleted,
		agentmetrics.NameRunDuration + "{outcome=" + agentmetrics.RunCompleted + "}",
	} {
		if got[key] != 0 {
			t.Errorf("%s = %v with no metrics wired; something other than the wiring is moving this series", key, got[key])
		}
	}
}

// TestEscalationReasonAgreesWithEscalationComment: the label an alert routes on
// and the words the human then reads on the board must describe the same
// situation. They are computed by two functions; this is what keeps them from
// drifting.
func TestEscalationReasonAgreesWithEscalationComment(t *testing.T) {
	for _, tc := range []struct {
		cause      error
		wantReason string
		wantPhrase string
	}{
		{agent.ErrNeedsHumanApproval, agentmetrics.ReasonVerifierFailed, "the verifier failed repeatedly"},
		{agent.ErrNoVerifierEvidence, agentmetrics.ReasonVerifierFailed, "no passing verifier evidence"},
		{agent.ErrBudgetExhaust, agentmetrics.ReasonBudgetExhausted, "did not finish successfully"},
		{errors.New("kaboom"), agentmetrics.ReasonRunError, "did not finish successfully"},
	} {
		if got := EscalationReason(tc.cause); got != tc.wantReason {
			t.Errorf("EscalationReason(%v) = %q, want %q", tc.cause, got, tc.wantReason)
		}
		if body := EscalationComment("agent", tc.cause, ""); !strings.Contains(body, tc.wantPhrase) {
			t.Errorf("EscalationComment(%v) does not say %q", tc.cause, tc.wantPhrase)
		}
	}
}

// TestEscalationReasonStaysInsideTheContract: whatever a run failure turns out
// to be, the label must be one of the three the contract froze.
func TestEscalationReasonStaysInsideTheContract(t *testing.T) {
	accepted := map[string]bool{
		agentmetrics.ReasonVerifierFailed:  true,
		agentmetrics.ReasonBudgetExhausted: true,
		agentmetrics.ReasonRunError:        true,
	}
	for _, cause := range []error{
		nil,
		errors.New("/home/jaslian/secret/path exploded"),
		fmt.Errorf("ticket T-9182 failed: %w", context.DeadlineExceeded),
		agent.ErrMaxIterations,
		agent.ErrTimeout,
	} {
		if got := EscalationReason(cause); !accepted[got] {
			t.Errorf("EscalationReason(%v) = %q, which is not in the frozen label domain", cause, got)
		}
	}
}
