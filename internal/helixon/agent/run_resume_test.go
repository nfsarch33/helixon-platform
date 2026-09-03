package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingExecutor records every dispatch so a test can prove exactly which
// tool calls a resumed run re-executed.
type countingExecutor struct {
	mu      sync.Mutex
	tools   []llm.Tool
	results map[string]string
	calls   []string // tool names in dispatch order
}

func (c *countingExecutor) Execute(_ context.Context, name, _ string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
	if r, ok := c.results[name]; ok {
		return r, nil
	}
	return "", errUnknownTool
}

func (c *countingExecutor) Available() []llm.Tool { return c.tools }

func (c *countingExecutor) dispatched() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

var errUnknownTool = assert.AnError

func toolCall(id, name string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: `{}`}}
}

func assistantWithTools(calls ...llm.ToolCall) *llm.CompletionResponse {
	return &llm.CompletionResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", ToolCalls: calls}}},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 5},
	}
}

func finalAnswer(text string) *llm.CompletionResponse {
	return &llm.CompletionResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: text}}},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 5},
	}
}

func newDurableAgent(t *testing.T, provider llm.Provider, tools ToolExecutor) (*Agent, *SessionStore, *fakeClock) {
	t.Helper()
	store, clock := newClockedStore(t, time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	a := New(provider, tools, store, Config{
		MaxIterations: 10, MaxTokens: 50000, Timeout: 10 * time.Second,
		Completion: CompletionPolicy{Enabled: false, MutatingTools: []string{"write"}},
	})
	return a, store, clock
}

// seedInterruptedRun writes the durable state a crash would leave behind:
// the run and user turn, one assistant turn requesting calls, a tool turn +
// done step for each id in recorded, and a pending step (no tool turn) for
// each id in pending. Everything else about the calls is untouched.
func seedInterruptedRun(t *testing.T, store *SessionStore, sessionID string, calls []llm.ToolCall, recorded, pending []string) {
	t.Helper()
	const runID = "run-1"
	ctx := context.Background()
	_, created, err := store.StartRun(ctx, runID, sessionID, "do the work", nil)
	require.NoError(t, err)
	require.True(t, created)
	tcJSON, err := json.Marshal(calls)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sessionID, RoleAssistant, "", tcJSON, "", 10, 5)
	require.NoError(t, err)
	byID := map[string]llm.ToolCall{}
	for _, c := range calls {
		byID[c.ID] = c
	}
	for _, id := range recorded {
		_, _, err := store.BeginStep(ctx, runID, 1, id, byID[id].Function.Name, "{}")
		require.NoError(t, err)
		_, err = store.AppendTurn(ctx, sessionID, RoleTool, "result-"+id, nil, id, 0, 0)
		require.NoError(t, err)
		require.NoError(t, store.FinishStep(ctx, runID, 1, id, StepDone, "result-"+id))
	}
	for _, id := range pending {
		_, _, err := store.BeginStep(ctx, runID, 1, id, byID[id].Function.Name, "{}")
		require.NoError(t, err)
	}
}

// TestRunDurable_RecordsRunAndSteps: a normal tool-call loop leaves a
// completed run row and a done step per tool call, with the result stored.
func TestRunDurable_RecordsRunAndSteps(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{
		assistantWithTools(toolCall("c1", "search")), finalAnswer("42"),
	}}
	tools := &countingExecutor{results: map[string]string{"search": "found"}}
	a, store, _ := newDurableAgent(t, provider, tools)
	sid := newRunSession(t, store)

	res, err := a.RunDurable(ctx, "run-1", sid, "question", map[string]string{"ticket_id": "T-1"})
	require.NoError(t, err)
	assert.Equal(t, "42", res.FinalContent)
	assert.Equal(t, 2, res.Iterations)

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunCompleted, run.Status)
	assert.Equal(t, "42", run.FinalContent)
	assert.Equal(t, 2, run.Iterations)
	assert.Equal(t, 1, run.Attempts)
	assert.Equal(t, "T-1", run.Meta["ticket_id"])
	steps, err := store.ListSteps(ctx, "run-1")
	require.NoError(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, StepDone, steps[0].Status)
	assert.Equal(t, "found", steps[0].Result)
}

// TestRun_LegacyEntryPointIsDurableToo: the original Run signature now runs
// through the same durable path, so every channel gets a run row and the
// turn shape the existing tests assert on is unchanged.
func TestRun_LegacyEntryPointIsDurableToo(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{finalAnswer("hi")}}
	a, store, _ := newDurableAgent(t, provider, &countingExecutor{})
	sid := newRunSession(t, store)
	res, err := a.Run(ctx, sid, "hello")
	require.NoError(t, err)
	assert.Equal(t, "hi", res.FinalContent)
	runs, err := store.ListInterruptedRuns(ctx)
	require.NoError(t, err)
	assert.Empty(t, runs, "the run finished, so nothing is left to resume")
	turns, err := store.ListTurns(ctx, sid, 0)
	require.NoError(t, err)
	assert.Len(t, turns, 2)
}

// TestRunDurable_SameRunIDIsIdempotent: invoking the same run id again after
// it completed returns the stored result and calls neither the model nor a
// tool. This is the "resumed run re-pays nothing" guarantee at its simplest.
func TestRunDurable_SameRunIDIsIdempotent(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{
		assistantWithTools(toolCall("c1", "search")), finalAnswer("42"),
	}}
	tools := &countingExecutor{results: map[string]string{"search": "found"}}
	a, store, _ := newDurableAgent(t, provider, tools)
	sid := newRunSession(t, store)

	first, err := a.RunDurable(ctx, "run-1", sid, "question", nil)
	require.NoError(t, err)
	callsAfterFirst := provider.callIdx
	dispatchedAfterFirst := len(tools.dispatched())

	second, err := a.RunDurable(ctx, "run-1", sid, "question", nil)
	require.NoError(t, err)
	assert.Equal(t, first.FinalContent, second.FinalContent)
	assert.Equal(t, callsAfterFirst, provider.callIdx, "no model call on a completed run")
	assert.Equal(t, dispatchedAfterFirst, len(tools.dispatched()), "no tool dispatch on a completed run")
}

// TestResume_ExecutesOnlyTheMissingToolCall is the core resume guarantee:
// the crash happened after call c1 was recorded and before c2 was begun, so
// resuming dispatches c2 exactly once, never c1, and then finishes.
func TestResume_ExecutesOnlyTheMissingToolCall(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{finalAnswer("done")}}
	tools := &countingExecutor{results: map[string]string{"search": "found", "fetch": "fetched"}}
	a, store, _ := newDurableAgent(t, provider, tools)
	sid := newRunSession(t, store)
	seedInterruptedRun(t, store, sid,
		[]llm.ToolCall{toolCall("c1", "search"), toolCall("c2", "fetch")},
		[]string{"c1"}, nil)

	res, err := a.Resume(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, "done", res.FinalContent)
	assert.Equal(t, []string{"fetch"}, tools.dispatched(), "only the missing call runs")

	steps, err := store.ListSteps(ctx, "run-1")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	assert.Equal(t, StepDone, steps[1].Status)
	turns, err := store.ListTurns(ctx, sid, 0)
	require.NoError(t, err)
	// user, assistant(tools), tool c1, tool c2, assistant(final)
	assert.Len(t, turns, 5)
	assert.Equal(t, "c2", turns[3].ToolCallID)
	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunCompleted, run.Status)
	assert.Equal(t, 2, run.Iterations, "one interrupted iteration plus the resumed one")
}

// TestResume_PendingMutatingStepEscalates: a mutating tool whose outcome is
// unknown (step pending, no tool turn) is never re-executed. The run stops
// for a human with a tool turn that says why, and the step is marked failed.
func TestResume_PendingMutatingStepEscalates(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{finalAnswer("should not be reached")}}
	tools := &countingExecutor{results: map[string]string{"write": "written"}}
	a, store, _ := newDurableAgent(t, provider, tools)
	sid := newRunSession(t, store)
	seedInterruptedRun(t, store, sid, []llm.ToolCall{toolCall("w1", "write")}, nil, []string{"w1"})

	res, err := a.Resume(ctx, "run-1")
	assert.ErrorIs(t, err, ErrInterruptedMutation)
	require.NotNil(t, res)
	assert.True(t, res.NeedsHumanApproval)
	assert.Empty(t, tools.dispatched(), "an unknown-outcome mutation is never re-run")
	assert.Equal(t, 0, provider.callIdx, "the model is not consulted")

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunNeedsHuman, run.Status)
	steps, err := store.ListSteps(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, StepFailed, steps[0].Status)
	turns, err := store.ListTurns(ctx, sid, 0)
	require.NoError(t, err)
	require.Len(t, turns, 3)
	assert.Equal(t, RoleTool, turns[2].Role)
	assert.Equal(t, "w1", turns[2].ToolCallID)
	assert.Contains(t, turns[2].Content, "interrupted")
}

// TestResume_PendingIdempotentStepReexecutes: a non-mutating tool with an
// unknown outcome is simply run again; re-reading is free of side effects.
func TestResume_PendingIdempotentStepReexecutes(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{finalAnswer("done")}}
	tools := &countingExecutor{results: map[string]string{"search": "found"}}
	a, store, _ := newDurableAgent(t, provider, tools)
	sid := newRunSession(t, store)
	seedInterruptedRun(t, store, sid, []llm.ToolCall{toolCall("c1", "search")}, nil, []string{"c1"})

	res, err := a.Resume(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, "done", res.FinalContent)
	assert.Equal(t, []string{"search"}, tools.dispatched())
	steps, err := store.ListSteps(ctx, "run-1")
	require.NoError(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, StepDone, steps[0].Status)
}

// TestResume_PendingStepWithRecordedToolTurnIsDone: the crash landed between
// the tool turn append and FinishStep. The tool turn IS the outcome, so the
// step is closed without dispatching anything.
func TestResume_PendingStepWithRecordedToolTurnIsDone(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{finalAnswer("done")}}
	tools := &countingExecutor{results: map[string]string{"write": "written"}}
	a, store, _ := newDurableAgent(t, provider, tools)
	sid := newRunSession(t, store)
	seedInterruptedRun(t, store, sid, []llm.ToolCall{toolCall("w1", "write")}, nil, []string{"w1"})
	_, err := store.AppendTurn(ctx, sid, RoleTool, "written", nil, "w1", 0, 0)
	require.NoError(t, err)

	res, err := a.Resume(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, "done", res.FinalContent)
	assert.Empty(t, tools.dispatched(), "the recorded tool turn is the outcome; nothing re-runs")
	steps, err := store.ListSteps(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, StepDone, steps[0].Status)
}

// TestResume_FinalAnswerRecordedButRunOpenCompletesWithoutTheModel: the
// crash landed after the final assistant turn was written and before the
// run was closed. Resuming closes it; the model is not called.
func TestResume_FinalAnswerRecordedButRunOpenCompletesWithoutTheModel(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{} // zero responses: any model call fails the test
	a, store, _ := newDurableAgent(t, provider, &countingExecutor{})
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "q", nil)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sid, RoleAssistant, "final words", nil, "", 10, 5)
	require.NoError(t, err)

	res, err := a.Resume(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, "final words", res.FinalContent)
	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunCompleted, run.Status)
}

// TestResume_FinishedRunReturnsStoredResult: Resume on an ended run returns
// the stored result and ErrRunFinished whatever the status, so a recovery
// sweep can tell "already finished" from "finished by me". RunDurable with
// the same id stays the idempotent, error-free path.
func TestResume_FinishedRunReturnsStoredResult(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{finalAnswer("42")}}
	a, store, _ := newDurableAgent(t, provider, &countingExecutor{})
	sid := newRunSession(t, store)
	_, err := a.RunDurable(ctx, "run-1", sid, "q", nil)
	require.NoError(t, err)

	res, err := a.Resume(ctx, "run-1")
	assert.ErrorIs(t, err, ErrRunFinished)
	require.NotNil(t, res)
	assert.Equal(t, "42", res.FinalContent)
	assert.Equal(t, 1, provider.callIdx, "no model call on a finished run")

	again, err := a.RunDurable(ctx, "run-1", sid, "q", nil)
	require.NoError(t, err)
	assert.Equal(t, "42", again.FinalContent)
}

// TestResume_UnknownRun: resuming an id nobody started is an error, not an
// empty run.
func TestResume_UnknownRun(t *testing.T) {
	a, _, _ := newDurableAgent(t, &mockProvider{}, &countingExecutor{})
	_, err := a.Resume(context.Background(), "ghost")
	assert.ErrorIs(t, err, ErrRunNotFound)
}

// slowProvider blocks until the context ends, standing in for a provider
// that outlives the run's deadline.
type slowProvider struct{}

//nolint:gocritic // hugeParam: the llm.Provider contract takes the request by value
func (slowProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRunDurable_DeadlineFailsTheRun: a run that exhausted its budget is a
// FAILED run, not an interrupted one. Resuming it on the next start would
// hand it a fresh budget and report its ticket a second time.
func TestRunDurable_DeadlineFailsTheRun(t *testing.T) {
	ctx := context.Background()
	store, clock := newClockedStore(t, time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	a := New(slowProvider{}, &countingExecutor{}, store, Config{
		MaxIterations: 3, MaxTokens: 1000, Timeout: 100 * time.Millisecond,
		Completion: CompletionPolicy{Enabled: false},
	})
	sid := newRunSession(t, store)

	_, err := a.RunDurable(ctx, "run-1", sid, "q", nil)
	require.Error(t, err)

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunFailed, run.Status, "a deadline is a terminal verdict")
	assert.NotEmpty(t, run.Err)
	assert.Equal(t, 1, run.Attempts)
	clock.Add(a.cfg.LeaseTTL + time.Second)
	interrupted, err := store.ListInterruptedRuns(ctx)
	require.NoError(t, err)
	assert.Empty(t, interrupted, "a failed run must not come back through recovery")
}

// TestRunDurable_CancellationLeavesTheRunResumable: a shutdown mid-run is not
// a verdict. The run stays running, its lease lapses, and a later worker
// finishes it from the durable log.
func TestRunDurable_CancellationLeavesTheRunResumable(t *testing.T) {
	store, clock := newClockedStore(t, time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	cfg := Config{
		MaxIterations: 3, MaxTokens: 1000, Timeout: 10 * time.Second,
		Completion: CompletionPolicy{Enabled: false},
	}
	a := New(slowProvider{}, &countingExecutor{}, store, cfg)
	sid := newRunSession(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := a.RunDurable(ctx, "run-1", sid, "q", nil)
	require.ErrorIs(t, err, context.Canceled)

	run, err := store.GetRun(context.Background(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunRunning, run.Status, "a canceled attempt leaves the run open")
	assert.Equal(t, 1, run.Attempts)

	clock.Add(a.cfg.LeaseTTL + time.Second) // the EFFECTIVE ttl: cfg.LeaseTTL is zero before defaults
	interrupted, err := store.ListInterruptedRuns(context.Background())
	require.NoError(t, err)
	require.Len(t, interrupted, 1)

	later := New(&mockProvider{responses: []*llm.CompletionResponse{finalAnswer("late but done")}}, &countingExecutor{}, store, cfg)
	res, err := later.Resume(context.Background(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, "late but done", res.FinalContent)
	run, err = store.GetRun(context.Background(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunCompleted, run.Status)
	assert.Equal(t, 2, run.Attempts)
}

// gateProvider blocks its first call until released, and tells the test when
// the call has been entered.
type gateProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

//nolint:gocritic // hugeParam: the llm.Provider contract takes the request by value
func (g *gateProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return finalAnswer("released"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestRunDurable_ConcurrentSameIDIsRefused: the lease cannot refuse a second
// entry by the SAME owner, so the in-process guard must. Two loops on one run
// in one process would be exactly the duplicated side effect S2 forbids.
func TestRunDurable_ConcurrentSameIDIsRefused(t *testing.T) {
	ctx := context.Background()
	gate := &gateProvider{entered: make(chan struct{}), release: make(chan struct{})}
	a, store, _ := newDurableAgent(t, gate, &countingExecutor{})
	sid := newRunSession(t, store)

	type outcome struct {
		res *RunResult
		err error
	}
	first := make(chan outcome, 1)
	go func() {
		res, err := a.RunDurable(ctx, "run-1", sid, "q", nil)
		first <- outcome{res, err}
	}()
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first run never reached the model")
	}

	_, err := a.RunDurable(ctx, "run-1", sid, "q", nil)
	assert.ErrorIs(t, err, ErrLeaseHeld, "a second loop on a run this process is already executing")
	_, err = a.Resume(ctx, "run-1")
	assert.ErrorIs(t, err, ErrLeaseHeld)

	close(gate.release)
	select {
	case out := <-first:
		require.NoError(t, out.err)
		assert.Equal(t, "released", out.res.FinalContent)
	case <-time.After(10 * time.Second):
		t.Fatal("the first run never finished")
	}
	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, RunCompleted, run.Status)
	assert.Equal(t, 1, run.Attempts, "the refused entries must not count as attempts")
}

// TestRunDurable_LeaseHeldByAnotherWorkerIsRefused: two workers, one run;
// the second gets ErrLeaseHeld and touches nothing.
func TestRunDurable_LeaseHeldByAnotherWorkerIsRefused(t *testing.T) {
	ctx := context.Background()
	provider := &mockProvider{responses: []*llm.CompletionResponse{finalAnswer("42")}}
	a, store, _ := newDurableAgent(t, provider, &countingExecutor{})
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "q", nil)
	require.NoError(t, err)
	ok, err := store.ClaimRun(ctx, "run-1", "other-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	_, err = a.Resume(ctx, "run-1")
	assert.ErrorIs(t, err, ErrLeaseHeld)
	assert.Equal(t, 0, provider.callIdx)
}

// TestRenewLease_LostLeaseCancelsTheRun: the renewal goroutine's contract.
// When the store refuses a renewal (the run was reclaimed after the lease
// lapsed), the run's context is canceled so the loop stops before it can
// write a stale result.
func TestRenewLease_LostLeaseCancelsTheRun(t *testing.T) {
	ctx := context.Background()
	a, store, clock := newDurableAgent(t, &mockProvider{}, &countingExecutor{})
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "run-1", sid, "q", nil)
	require.NoError(t, err)
	ok, err := store.ClaimRun(ctx, "run-1", a.owner, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	assert.True(t, a.renewOnce(runCtx, cancel, "run-1"), "a live lease renews")
	assert.NoError(t, runCtx.Err())

	clock.Add(2 * time.Minute)
	ok, err = store.ClaimRun(ctx, "run-1", "other-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, a.renewOnce(runCtx, cancel, "run-1"), "a lost lease does not renew")
	assert.ErrorIs(t, runCtx.Err(), context.Canceled, "and the run is canceled")
}

// TestDeadLetterExhausted: interrupted runs that already spent their attempt
// budget are failed, not resumed forever.
func TestDeadLetterExhausted(t *testing.T) {
	ctx := context.Background()
	store, clock := newRunStore(t)
	sid := newRunSession(t, store)
	_, _, err := store.StartRun(ctx, "spent", sid, "q", nil)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		ok, err := store.ClaimRun(ctx, "spent", "w", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
	}
	_, _, err = store.StartRun(ctx, "fresh", sid, "q", nil)
	require.NoError(t, err)
	clock.Add(2 * time.Minute)

	n, err := store.DeadLetterExhausted(ctx, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	spent, err := store.GetRun(ctx, "spent")
	require.NoError(t, err)
	assert.Equal(t, RunFailed, spent.Status)
	assert.Contains(t, spent.Err, "attempts")
	left, err := store.ListInterruptedRuns(ctx)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, "fresh", left[0].ID)
}
