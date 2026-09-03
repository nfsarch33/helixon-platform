package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRunStore opens a session store on a temp file with a caller-driven clock,
// so lease expiry is tested by moving time, never by sleeping toward it.
func newRunStore(t *testing.T) (*SessionStore, *fakeClock) {
	t.Helper()
	return newClockedStore(t, time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC))
}

func newRunSession(t *testing.T, store *SessionStore) string {
	t.Helper()
	sess, err := store.CreateSession(context.Background(), "durable-agent", nil)
	require.NoError(t, err)
	return sess.ID
}

// TestStartRun_IsIdempotent: starting the same run id twice creates ONE run
// and ONE user turn. A caller that retried after a lost reply must not pay
// for a second conversation.
func TestStartRun_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunStore(t)
	sid := newRunSession(t, store)

	run, created, err := store.StartRun(ctx, "run-1", sid, "do the thing", map[string]string{"ticket_id": "T-9"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, RunRunning, run.Status)
	assert.Equal(t, "T-9", run.Meta["ticket_id"])

	again, created, err := store.StartRun(ctx, "run-1", sid, "do the thing", nil)
	require.NoError(t, err)
	assert.False(t, created, "second start must find the existing run")
	assert.Equal(t, run.ID, again.ID)
	assert.Equal(t, "do the thing", again.UserMessage)

	turns, err := store.ListTurns(ctx, sid, 0)
	require.NoError(t, err)
	require.Len(t, turns, 1, "exactly one user turn for one run id")
	assert.Equal(t, RoleUser, turns[0].Role)
}

// TestStartRun_UserTurnAndRunRowAreOneTransaction: the run row never exists
// without its user turn and vice versa.
func TestStartRun_UserTurnAndRunRowAreOneTransaction(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunStore(t)
	_, _, err := store.StartRun(ctx, "run-x", "no-such-session", "hello", nil)
	require.Error(t, err, "a run on an unknown session must be rejected by the foreign key")
	_, err = store.GetRun(ctx, "run-x")
	assert.ErrorIs(t, err, ErrRunNotFound, "no orphan run row after the rejected start")
}

// TestClaimRun_ExactlyOneOwnerUntilTheLeaseExpires: the CAS claim admits one
// owner; a second claimant is refused while the lease is live and admitted
// once the clock passes lease_until. attempts counts every successful claim,
// so a retry budget survives the crash that made the retry necessary.
func TestClaimRun_ExactlyOneOwnerUntilTheLeaseExpires(t *testing.T) {
	ctx := context.Background()
	store, clock := newRunStore(t)
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "work", nil)
	require.NoError(t, err)

	ok, err := store.ClaimRun(ctx, "run-1", "worker-a", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "first claim wins")

	ok, err = store.ClaimRun(ctx, "run-1", "worker-b", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "a live lease refuses a second owner")

	ok, err = store.ClaimRun(ctx, "run-1", "worker-a", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "the owner may re-claim (restart of the same worker identity)")

	clock.Add(2 * time.Minute)
	ok, err = store.ClaimRun(ctx, "run-1", "worker-b", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "an expired lease is reclaimable")

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, "worker-b", run.Owner)
	assert.Equal(t, 3, run.Attempts, "every successful claim is an attempt")
}

// TestRenewRun_OnlyTheOwnerExtends: a renewal by a worker that lost the
// lease returns false, which is the executor's signal to stop.
func TestRenewRun_OnlyTheOwnerExtends(t *testing.T) {
	ctx := context.Background()
	store, clock := newRunStore(t)
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "work", nil)
	require.NoError(t, err)
	ok, err := store.ClaimRun(ctx, "run-1", "worker-a", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = store.RenewRun(ctx, "run-1", "worker-a", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	clock.Add(2 * time.Minute)
	ok, err = store.ClaimRun(ctx, "run-1", "worker-b", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = store.RenewRun(ctx, "run-1", "worker-a", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "the reclaimed worker must not extend a lease it lost")
}

// TestFinishRun_OwnerGuardedAndTerminal: only the lease owner may finish; a
// finished run refuses further claims and returns its stored result.
func TestFinishRun_OwnerGuardedAndTerminal(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunStore(t)
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "work", nil)
	require.NoError(t, err)
	ok, err := store.ClaimRun(ctx, "run-1", "worker-a", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	res := &RunResult{SessionID: sid, FinalContent: "42", Iterations: 3, TokensIn: 10, TokensOut: 5}
	ok, err = store.FinishRun(ctx, "run-1", "worker-b", RunCompleted, res, nil)
	require.NoError(t, err)
	assert.False(t, ok, "a non-owner cannot finish")

	ok, err = store.FinishRun(ctx, "run-1", "worker-a", RunCompleted, res, nil)
	require.NoError(t, err)
	assert.True(t, ok)

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunCompleted, run.Status)
	assert.Equal(t, "42", run.FinalContent)
	assert.Equal(t, 3, run.Iterations)
	assert.Equal(t, 10, run.TokensIn)

	ok, err = store.ClaimRun(ctx, "run-1", "worker-c", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "a completed run is never claimed again")
}

// TestFinishRun_FailedKeepsTheError: a failed run records its error string
// and its terminal status, so a resume does not re-run a policy stop.
func TestFinishRun_FailedKeepsTheError(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunStore(t)
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "work", nil)
	require.NoError(t, err)
	ok, err := store.ClaimRun(ctx, "run-1", "w", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = store.FinishRun(ctx, "run-1", "w", RunFailed, &RunResult{SessionID: sid}, ErrBudgetExhaust)
	require.NoError(t, err)
	require.True(t, ok)
	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunFailed, run.Status)
	assert.Contains(t, run.Err, "budget exhausted")
}

// TestListInterruptedRuns_ExpiredLeasesOnly: running runs whose lease has
// lapsed are the resumable set; live leases and finished runs are not.
func TestListInterruptedRuns_ExpiredLeasesOnly(t *testing.T) {
	ctx := context.Background()
	store, clock := newRunStore(t)
	sid := newRunSession(t, store)
	for _, id := range []string{"live", "dead", "done", "never-claimed"} {
		_, _, err := store.StartRun(ctx, id, sid, "work "+id, nil)
		require.NoError(t, err)
	}
	ok, err := store.ClaimRun(ctx, "dead", "w", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.ClaimRun(ctx, "done", "w", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.FinishRun(ctx, "done", "w", RunCompleted, &RunResult{SessionID: sid}, nil)
	require.NoError(t, err)
	require.True(t, ok)
	clock.Add(2 * time.Minute)
	ok, err = store.ClaimRun(ctx, "live", "w", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	got, err := store.ListInterruptedRuns(ctx)
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	assert.ElementsMatch(t, []string{"dead", "never-claimed"}, ids,
		"a run that was never claimed (crash before the claim) is interrupted too")
}

// TestSteps_IdempotentByToolCallID: BeginStep for the same tool call returns
// the existing step instead of a second one; seq is dense per run; FinishStep
// records the outcome; ListSteps is in seq order.
func TestSteps_IdempotentByToolCallID(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunStore(t)
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "work", nil)
	require.NoError(t, err)

	s1, created, err := store.BeginStep(ctx, "run-1", 1, "call_1", "search", `{"q":"a"}`)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, int64(1), s1.Seq)
	assert.Equal(t, StepPending, s1.Status)

	s1again, created, err := store.BeginStep(ctx, "run-1", 1, "call_1", "search", `{"q":"a"}`)
	require.NoError(t, err)
	assert.False(t, created, "the same tool call is one step")
	assert.Equal(t, s1.Seq, s1again.Seq)

	s2, created, err := store.BeginStep(ctx, "run-1", 1, "call_2", "write", `{}`)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, int64(2), s2.Seq, "seq is dense per run")

	require.NoError(t, store.FinishStep(ctx, "run-1", 1, "call_1", StepDone, "result-1"))
	steps, err := store.ListSteps(ctx, "run-1")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	assert.Equal(t, StepDone, steps[0].Status)
	assert.Equal(t, "result-1", steps[0].Result)
	assert.Equal(t, StepPending, steps[1].Status)
	assert.Equal(t, "write", steps[1].Tool)

	// The same tool call id in a LATER iteration is a different step: some
	// providers emit "call_0" on every response.
	s3, created, err := store.BeginStep(ctx, "run-1", 2, "call_1", "search", `{"q":"b"}`)
	require.NoError(t, err)
	assert.True(t, created, "iteration scopes the step key")
	assert.Equal(t, int64(3), s3.Seq)
}

// TestFinishStep_UnknownStepIsAnError: finishing a step that was never begun
// is a programming error, not a silent insert.
func TestFinishStep_UnknownStepIsAnError(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunStore(t)
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "work", nil)
	require.NoError(t, err)
	err = store.FinishStep(ctx, "run-1", 1, "nope", StepDone, "")
	assert.ErrorIs(t, err, ErrStepNotFound)
}

// TestRunTablesMigration_IsIdempotentAndUpgradesLegacyDatabases: a database
// created before the run tables existed gains them on open, and opening it
// again is a no-op.
func TestRunTablesMigration_IsIdempotentAndUpgradesLegacyDatabases(t *testing.T) {
	ctx := context.Background()
	dsn := testDBPath(t, "legacy.db")

	first, err := NewSessionStore(ctx, dsn)
	require.NoError(t, err)
	_, err = first.db.ExecContext(ctx, `DROP TABLE run_steps; DROP TABLE runs;`)
	require.NoError(t, err, "simulate a database from before the run tables")
	require.NoError(t, first.Close())

	second, err := NewSessionStore(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = second.Close() }()
	sid := newRunSession(t, second)
	_, created, err := second.StartRun(ctx, "run-1", sid, "work", nil)
	require.NoError(t, err)
	assert.True(t, created)

	require.NoError(t, second.Close())
	third, err := NewSessionStore(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = third.Close() }()
	run, err := third.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, "work", run.UserMessage)
}

// TestStartRunClaimed_BornClaimed: the row exists claimed from its first
// instant, so ListInterruptedRuns never sees a brand-new live run, and a
// second call with the same id neither re-claims nor counts an attempt.
func TestStartRunClaimed_BornClaimed(t *testing.T) {
	ctx := context.Background()
	store, clock := newClockedStore(t, time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	sid := newRunSession(t, store)

	run, created, err := store.StartRunClaimed(ctx, "run-1", sid, "q", nil, "worker-a", time.Minute)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, "worker-a", run.Owner)
	assert.Equal(t, 1, run.Attempts)
	assert.True(t, run.LeaseUntil.After(clock.Now()))

	interrupted, err := store.ListInterruptedRuns(ctx)
	require.NoError(t, err)
	assert.Empty(t, interrupted, "a live, claimed run is not interrupted")

	again, created, err := store.StartRunClaimed(ctx, "run-1", sid, "q", nil, "worker-b", time.Minute)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "worker-a", again.Owner, "an existing run is not re-claimed by StartRunClaimed")
	assert.Equal(t, 1, again.Attempts)

	_, _, err = store.StartRunClaimed(ctx, "run-2", sid, "q", nil, "", time.Minute)
	assert.Error(t, err, "an empty owner cannot claim")
}

// TestAppendRunTurn_ZombieIsRefused: once another worker has reclaimed the
// run, the old owner's writes are refused in the same transaction that
// checks the lease. Without this a worker blocked past its TTL could append
// turns to a conversation it no longer owns before its renewal tick told it.
func TestAppendRunTurn_ZombieIsRefused(t *testing.T) {
	ctx := context.Background()
	store, clock := newClockedStore(t, time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	sid := newRunSession(t, store)
	_, _, err := store.StartRunClaimed(ctx, "run-1", sid, "q", nil, "worker-a", time.Minute)
	require.NoError(t, err)

	_, err = store.AppendRunTurn(ctx, "run-1", "worker-a", sid, RoleAssistant, "thinking", nil, "", 1, 1)
	require.NoError(t, err, "the live owner writes")

	clock.Add(2 * time.Minute) // worker-a's lease lapses
	ok, err := store.ClaimRun(ctx, "run-1", "worker-b", time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "worker-b reclaims the lapsed lease")

	_, err = store.AppendRunTurn(ctx, "run-1", "worker-a", sid, RoleTool, "late result", nil, "c1", 0, 0)
	assert.ErrorIs(t, err, ErrLeaseLost, "the zombie's write is refused")
	_, err = store.AppendRunTurn(ctx, "run-1", "worker-b", sid, RoleTool, "result", nil, "c1", 0, 0)
	require.NoError(t, err, "the new owner writes")

	turns, err := store.ListTurns(ctx, sid, 0)
	require.NoError(t, err)
	require.Len(t, turns, 3, "user turn + the two accepted writes; nothing from the zombie")
	assert.Equal(t, "result", turns[2].Content)

	ok, err = store.FinishRun(ctx, "run-1", "worker-b", RunCompleted, &RunResult{FinalContent: "done"}, nil)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = store.AppendRunTurn(ctx, "run-1", "worker-b", sid, RoleAssistant, "after the end", nil, "", 0, 0)
	assert.ErrorIs(t, err, ErrLeaseLost, "a finished run accepts no more turns")
}

// TestListRuns_NewestFirstFilteredAndCapped: the console's run list is newest
// first, filterable by status, and capped.
func TestListRuns_NewestFirstFilteredAndCapped(t *testing.T) {
	ctx := context.Background()
	store, clock := newClockedStore(t, time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC))
	sid := newRunSession(t, store)
	for i := 1; i <= 4; i++ {
		clock.Add(time.Second)
		if _, _, err := store.StartRun(ctx, fmt.Sprintf("run-%d", i), sid, "q", nil); err != nil {
			t.Fatal(err)
		}
	}
	ok, err := store.ClaimRun(ctx, "run-2", "w", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.FinishRun(ctx, "run-2", "w", RunCompleted, &RunResult{TokensIn: 10, TokensOut: 5}, nil)
	require.NoError(t, err)
	require.True(t, ok)

	all, err := store.ListRuns(ctx, RunFilter{})
	require.NoError(t, err)
	require.Len(t, all, 4)
	assert.Equal(t, []string{"run-4", "run-3", "run-2", "run-1"}, []string{all[0].ID, all[1].ID, all[2].ID, all[3].ID})

	done, err := store.ListRuns(ctx, RunFilter{Status: RunCompleted})
	require.NoError(t, err)
	require.Len(t, done, 1)
	assert.Equal(t, "run-2", done[0].ID)

	capped, err := store.ListRuns(ctx, RunFilter{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, capped, 2)
}

// TestRunUsage_SumsTokensByStatusSince: costs come from the runs table, per
// status, and only for runs created at or after the window start.
func TestRunUsage_SumsTokensByStatusSince(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	store, clock := newClockedStore(t, start)
	sid := newRunSession(t, store)
	finish := func(id string, status RunStatus, in, out int) {
		t.Helper()
		_, _, err := store.StartRun(ctx, id, sid, "q", nil)
		require.NoError(t, err)
		ok, err := store.ClaimRun(ctx, id, "w", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		ok, err = store.FinishRun(ctx, id, "w", status, &RunResult{TokensIn: in, TokensOut: out}, nil)
		require.NoError(t, err)
		require.True(t, ok)
	}
	finish("old", RunCompleted, 100, 50)
	clock.Add(2 * time.Hour)
	finish("new-ok", RunCompleted, 30, 20)
	finish("new-bad", RunFailed, 7, 3)
	_, _, err := store.StartRun(ctx, "new-live", sid, "q", nil)
	require.NoError(t, err)

	u, err := store.RunUsage(ctx, start.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 3, u.Runs)
	assert.Equal(t, 1, u.Completed)
	assert.Equal(t, 1, u.Failed)
	assert.Equal(t, 1, u.Running)
	assert.Equal(t, 37, u.TokensIn)
	assert.Equal(t, 23, u.TokensOut)

	all, err := store.RunUsage(ctx, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 4, all.Runs)
	assert.Equal(t, 137, all.TokensIn)
}
