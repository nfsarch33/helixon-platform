package helixon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// newRecoveryRuntime builds a configured runtime with a fake board, ticket
// polling enabled (so a poller exists to report through) but NOT started:
// recovery is exercised directly, the way Run() invokes it.
func newRecoveryRuntime(t *testing.T) (*Runtime, *fakeBoard, *recordingProvider) {
	t.Helper()
	board := newFakeBoard()
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "recovery-agent",
	}, quietLogger())
	cfg := RuntimeConfig{
		AgentID:    "recovery-agent",
		SessionDSN: "file::memory:?cache=shared",
		Timeout:    5 * time.Second,
		Logger:     quietLogger(),
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

// modelCalls counts the requests a recordingProvider has seen.
func modelCalls(p *recordingProvider) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.messages)
}

// seedTicketRun writes the durable state of a ticket run whose worker died.
func seedTicketRun(t *testing.T, rt *Runtime, runID, ticketID string) string {
	t.Helper()
	ctx := context.Background()
	sess, err := rt.store.CreateSession(ctx, rt.cfg.AgentID, map[string]string{"channel": "ticket", "ticket_id": ticketID})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, _, err := rt.store.StartRun(ctx, runID, sess.ID, "work the ticket", map[string]string{"channel": "ticket", "ticket_id": ticketID}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return sess.ID
}

// TestRecoverInterruptedRuns_CompletesTheTicketFromTheDurableLog: the worker
// died after the final answer was written and before the run was closed.
// Recovery closes the run WITHOUT a model call and reports the evidence to
// the board, so the ticket is not stuck "claimed" forever.
func TestRecoverInterruptedRuns_CompletesTheTicketFromTheDurableLog(t *testing.T) {
	rt, board, prov := newRecoveryRuntime(t)
	ctx := context.Background()
	sid := seedTicketRun(t, rt, "run-t7", "T-7")
	if _, err := rt.store.AppendTurn(ctx, sid, agent.RoleAssistant, "evidence: all checks green", nil, "", 10, 5); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	stats, err := rt.RecoverInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedRuns: %v", err)
	}
	if stats.Resumed != 1 || stats.Completed != 1 || stats.Escalated != 0 {
		t.Fatalf("stats = %+v, want 1 resumed, 1 completed", stats)
	}
	_, completed, comments := board.snapshot()
	if completed["T-7"] != "evidence: all checks green" {
		t.Fatalf("board completed = %v, want T-7 with the recovered evidence", completed)
	}
	if len(comments["T-7"]) != 0 {
		t.Fatalf("no escalation comment expected, got %v", comments["T-7"])
	}
	run, err := rt.store.GetRun(ctx, "run-t7")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != agent.RunCompleted {
		t.Fatalf("run status = %s, want completed", run.Status)
	}
	if got := modelCalls(prov); got != 0 {
		t.Fatalf("provider was called %d times; a recorded final answer needs no model", got)
	}
}

// TestRecoverInterruptedRuns_EscalatesAnUnknownMutation: the worker died
// with a mutating tool call dispatched and no outcome recorded. Recovery must
// not re-run it; it stops the run for a human and escalates on the board.
func TestRecoverInterruptedRuns_EscalatesAnUnknownMutation(t *testing.T) {
	rt, board, _ := newRecoveryRuntime(t)
	ctx := context.Background()
	sid := seedTicketRun(t, rt, "run-t8", "T-8")
	calls := []llm.ToolCall{{ID: "sh1", Type: "function", Function: llm.FunctionCall{Name: "shell", Arguments: `{"cmd":"rm -rf build"}`}}}
	tcJSON, _ := json.Marshal(calls)
	if _, err := rt.store.AppendTurn(ctx, sid, agent.RoleAssistant, "", tcJSON, "", 10, 5); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if _, _, err := rt.store.BeginStep(ctx, "run-t8", 1, "sh1", "shell", `{"cmd":"rm -rf build"}`); err != nil {
		t.Fatalf("BeginStep: %v", err)
	}

	stats, err := rt.RecoverInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedRuns: %v", err)
	}
	if stats.Resumed != 1 || stats.Escalated != 1 || stats.Completed != 0 {
		t.Fatalf("stats = %+v, want 1 resumed, 1 escalated", stats)
	}
	_, completed, comments := board.snapshot()
	if _, ok := completed["T-8"]; ok {
		t.Fatal("a run stopped for a human must not complete the ticket")
	}
	if len(comments["T-8"]) != 1 {
		t.Fatalf("want one escalation comment on T-8, got %v", comments["T-8"])
	}
	run, err := rt.store.GetRun(ctx, "run-t8")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != agent.RunNeedsHuman {
		t.Fatalf("run status = %s, want needs_human", run.Status)
	}
}

// TestRecoverInterruptedRuns_DeadLettersSpentRuns: a run that already used
// its attempt budget is failed, not resumed again.
func TestRecoverInterruptedRuns_DeadLettersSpentRuns(t *testing.T) {
	rt, board, _ := newRecoveryRuntime(t)
	ctx := context.Background()
	seedTicketRun(t, rt, "run-t9", "T-9")
	for i := 0; i < 3; i++ {
		ok, err := rt.store.ClaimRun(ctx, "run-t9", "dead-worker", time.Nanosecond)
		if err != nil || !ok {
			t.Fatalf("ClaimRun %d: ok=%v err=%v", i, ok, err)
		}
	}
	time.Sleep(2 * time.Millisecond) // the nanosecond lease has lapsed

	stats, err := rt.RecoverInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedRuns: %v", err)
	}
	if stats.DeadLettered != 1 || stats.Resumed != 0 {
		t.Fatalf("stats = %+v, want 1 dead-lettered, 0 resumed", stats)
	}
	run, err := rt.store.GetRun(ctx, "run-t9")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != agent.RunFailed {
		t.Fatalf("run status = %s, want failed", run.Status)
	}
	_, completed, _ := board.snapshot()
	if _, ok := completed["T-9"]; ok {
		t.Fatal("a dead-lettered run must not complete its ticket")
	}
}

// TestRecoverInterruptedRuns_NothingToDo: a clean store is a no-op.
func TestRecoverInterruptedRuns_NothingToDo(t *testing.T) {
	rt, _, _ := newRecoveryRuntime(t)
	stats, err := rt.RecoverInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("RecoverInterruptedRuns: %v", err)
	}
	if stats != (RecoveryStats{}) {
		t.Fatalf("stats = %+v, want all zero", stats)
	}
}
