package helixon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"go.uber.org/goleak"
)

// The periodic recovery sweep: a run abandoned by a lost run-store lease is
// resumed within one interval, exactly once, and reported through
// ReportRecovered; a re-claimed ticket with an interrupted run resumes it
// instead of forking a second run; the loop exits cleanly on cancel.

// newTickRuntime is newRecoveryRuntime with a configurable recovery
// interval, so the tests can shrink the sweep period to milliseconds.
func newTickRuntime(t *testing.T, interval time.Duration) (*Runtime, *fakeBoard, *recordingProvider) {
	t.Helper()
	board := newFakeBoard()
	srv := board.server(t)
	t.Cleanup(srv.Close)
	// File DSN under t.TempDir(): shared-cache in-memory DSNs lose their
	// tables across runs in one process (-count/-shuffle proof).
	cfg0 := filepath.Join(t.TempDir(), "tick.db")
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "recovery-agent",
	}, quietLogger())
	cfg := RuntimeConfig{
		AgentID:          "recovery-agent",
		SessionDSN:       cfg0,
		Timeout:          5 * time.Second,
		Logger:           quietLogger(),
		RecoveryInterval: interval,
		Tickets: TicketPollerConfig{
			Enabled: true, Interval: time.Millisecond, MaxConcurrent: 1, TicketTimeout: 30 * time.Second,
		},
	}
	prov := &recordingProvider{reply: "model must not be consulted"}
	rt := NewRuntime(prov, cfg)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := rt.Configure(ctx, WithSprintboard(client)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })
	return rt, board, prov
}

// waitUntil polls cond until it holds or the deadline expires.
func waitUntil(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestRecoveryLoopResumesAbandonedRunExactlyOnce: an interrupted run whose
// worker recorded a final answer is resumed by the periodic sweep without a
// model call, reported through the poller's recovered path exactly once, and
// never picked up again on later ticks.
func TestRecoveryLoopResumesAbandonedRunExactlyOnce(t *testing.T) {
	// Registered FIRST so LIFO runs it LAST, after the runtime's and the
	// fake board's own cleanups have torn their goroutines down. The
	// baseline is snapshotted at test start: neighbours that leak (the
	// known shared-DSN fixture family) must not fail THIS test's check,
	// which exists to catch this loop's own goroutines.
	baseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
	rt, board, prov := newTickRuntime(t, 5*time.Millisecond)
	// The harness's Shutdown does not close the store's DB pool; goleak
	// (registered above, so it runs last) would otherwise see the pool's
	// connectionOpener.
	t.Cleanup(func() { _ = rt.store.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sid := seedTicketRun(t, rt, "run-tick", "T-TICK")
	if _, err := rt.store.AppendTurn(ctx, sid, agent.RoleAssistant, "evidence: recovered by the tick", nil, "", 10, 5); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	done := make(chan struct{})
	go func() { defer close(done); rt.recoveryLoop(ctx) }()
	defer func() { <-done }()

	waitUntil(t, 2*time.Second, func() bool {
		board.mu.Lock()
		defer board.mu.Unlock()
		return board.completions["T-TICK"] > 0
	}, "the sweep to complete the abandoned ticket")

	// Let several more intervals fire: the finished run must not be
	// re-reported (Resume on a finished run is ErrRunFinished, which is not
	// a report).
	time.Sleep(40 * time.Millisecond)
	cancel()
	<-done

	board.mu.Lock()
	n := board.completions["T-TICK"]
	ev := board.completed["T-TICK"]
	renewed := false
	for _, r := range board.renewals {
		if strings.HasPrefix(r, "T-TICK ") {
			renewed = true
		}
	}
	board.mu.Unlock()
	if !renewed {
		t.Fatalf("the recovery resume never renewed the board claim (renewals: %v)", board.renewals)
	}
	if n != 1 {
		t.Fatalf("ticket completed %d times, want exactly 1", n)
	}
	if ev != "evidence: recovered by the tick" {
		t.Fatalf("completed evidence = %q", ev)
	}
	run, err := rt.store.GetRun(context.Background(), "run-tick")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != agent.RunCompleted {
		t.Fatalf("run status = %s, want completed", run.Status)
	}
	if got := modelCalls(prov); got != 0 {
		t.Fatalf("provider called %d times; a recorded final answer needs no model", got)
	}
}

// TestRunTicketWorkResumesInterruptedRunPerTicket: the poller re-claimed a
// ticket whose earlier attempt is still resumable; the ticket path must
// resume THAT run and create no second one.
func TestRunTicketWorkResumesInterruptedRunPerTicket(t *testing.T) {
	rt, _, prov := newTickRuntime(t, -1) // no periodic sweep; the guard is under test directly
	ctx := context.Background()
	sid := seedTicketRun(t, rt, "run-prev", "T-PREV")
	if _, err := rt.store.AppendTurn(ctx, sid, agent.RoleAssistant, "evidence: resumed, not restarted", nil, "", 10, 5); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	final, err := rt.runTicketWork(ctx, controlplane.Ticket{ID: "T-PREV", Title: "re-claimed ticket"})
	if err != nil {
		t.Fatalf("runTicketWork: %v", err)
	}
	if final != "evidence: resumed, not restarted" {
		t.Fatalf("final = %q, want the resumed run's recorded answer", final)
	}
	run, gerr := rt.store.GetRun(ctx, "run-prev")
	if gerr != nil {
		t.Fatalf("GetRun: %v", gerr)
	}
	if run.Status != agent.RunCompleted {
		t.Fatalf("previous run status = %s, want completed", run.Status)
	}
	// No second run for the ticket: nothing left interrupted, and the model
	// was never consulted (a fresh run would have called it).
	if prev, ferr := rt.store.FindRunByTicket(ctx, "T-PREV"); ferr != nil || prev != nil {
		t.Fatalf("interrupted run for T-PREV after the ticket path: %v, %v; want none", prev, ferr)
	}
	if got := modelCalls(prov); got != 0 {
		t.Fatalf("provider called %d times; the ticket path resumed instead of starting a run", got)
	}
}

// TestRunTicketWorkDoesNotForkALiveRunOfTheTicket: the guard's "any
// non-terminal run" rule. A run whose lease is still alive - executing in
// another process - is the ticket's run; the ticket path finds it, is
// refused by the foreign lease, and never starts a second run beside it.
func TestRunTicketWorkDoesNotForkALiveRunOfTheTicket(t *testing.T) {
	rt, _, prov := newTickRuntime(t, -1) // no periodic sweep; the guard is under test directly
	ctx := context.Background()
	sid := seedTicketRun(t, rt, "run-live", "T-LIVE")
	if _, err := rt.store.AppendTurn(ctx, sid, agent.RoleAssistant, "work in flight elsewhere", nil, "", 10, 5); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	ok, err := rt.store.ClaimRun(ctx, "run-live", "another-worker", time.Hour)
	if err != nil || !ok {
		t.Fatalf("ClaimRun by the other worker: ok=%v err=%v", ok, err)
	}

	if _, rerr := rt.runTicketWork(ctx, controlplane.Ticket{ID: "T-LIVE", Title: "re-claimed while live"}); !errors.Is(rerr, agent.ErrLeaseHeld) {
		t.Fatalf("runTicketWork err = %v, want ErrLeaseHeld (the live run belongs to another worker)", rerr)
	}
	runs, lerr := rt.store.ListRunningRuns(ctx)
	if lerr != nil {
		t.Fatalf("ListRunningRuns: %v", lerr)
	}
	if len(runs) != 1 || runs[0].ID != "run-live" {
		t.Fatalf("runs of the ticket after the ticket path = %v, want exactly the original live run", ids(runs))
	}
	if got := modelCalls(prov); got != 0 {
		t.Fatalf("provider called %d times; no second run may start beside a live one", got)
	}
}

func ids(runs []agent.RunRecord) []string {
	out := make([]string, len(runs))
	for i := range runs {
		out[i] = runs[i].ID
	}
	return out
}
