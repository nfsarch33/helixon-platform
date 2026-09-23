package helixon

import (
	"context"
	"net/http"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
)

// Recovery renews the claim before resuming an interrupted ticket run. A 409
// means another holder owns the ticket now: the run is terminalised without a
// model call and the ticket is never completed by this agent.
func TestRecoverInterruptedRuns_LeaseLostTerminalisesWithoutCompleting(t *testing.T) {
	rt, board, prov := newRecoveryRuntime(t)
	board.mu.Lock()
	board.renewStatus = http.StatusConflict
	board.mu.Unlock()

	ctx := context.Background()
	sid := seedTicketRun(t, rt, "run-lost", "T-LOST")
	if _, err := rt.store.AppendTurn(ctx, sid, agent.RoleAssistant, "evidence: all checks green", nil, "", 10, 5); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	stats, err := rt.RecoverInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedRuns: %v", err)
	}
	_, completed, _ := board.snapshot()
	if _, ok := completed["T-LOST"]; ok {
		t.Fatalf("recovery completed a ticket whose claim was lost: %v", completed)
	}
	if stats.Resumed != 0 || stats.Completed != 0 || stats.Failed != 1 {
		t.Fatalf("stats = %+v, want 0 resumed, 0 completed, 1 failed", stats)
	}
	run, gerr := rt.store.GetRun(ctx, "run-lost")
	if gerr != nil {
		t.Fatalf("GetRun: %v", gerr)
	}
	if run.Status != agent.RunFailed {
		t.Fatalf("run status = %s, want %s", run.Status, agent.RunFailed)
	}
	if got := modelCalls(prov); got != 0 {
		t.Fatalf("provider called %d times, want 0", got)
	}
}
