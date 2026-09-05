package fleet

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustTaskStore opens a store and closes it at test end. Build the handler
// AFTER calling this: cleanups run last-registered-first, so newHandler's
// shutdown then joins the task goroutines before this close runs.
func mustTaskStore(t *testing.T, dsn string) *TaskStore {
	t.Helper()
	store, err := NewTaskStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestHandlerDurableCompleteSurvivesRestart is the property the in-memory map
// never had: a completed task's result is readable by a brand-new handler in
// a brand-new "process" (a second store handle on the same file).
func TestHandlerDurableCompleteSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "tasks.db")

	exec := &mockExecutor{result: "durable result"}
	storeA := mustTaskStore(t, dsn)
	a := newHandler(t, exec, nil, HandlerConfig{Store: storeA})

	taskID, err := a.Submit(ctx, TaskSubmission{AgentName: "durable", Prompt: "persist me"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		rec, ok := a.GetTask(taskID)
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)

	// Simulate the process exiting. The handler is joined first: a task
	// goroutine is still unwinding after the status it wrote becomes
	// visible, and closing the database under it is the bug this file's
	// shutdown wiring exists to prevent.
	require.NoError(t, a.Shutdown(ctx))
	require.NoError(t, storeA.Close())

	storeB := mustTaskStore(t, dsn)
	b := newHandler(t, &mockExecutor{result: "should never run"}, nil, HandlerConfig{Store: storeB})

	rec, ok := b.GetTask(taskID)
	require.True(t, ok, "a restarted handler must see the previous process's tasks")
	assert.Equal(t, TaskStatusCompleted, rec.Status)
	assert.Equal(t, "durable result", rec.Result)
	assert.Equal(t, 1, rec.Attempts)

	list := b.ListTasks()
	require.Len(t, list, 1)
	assert.Equal(t, taskID, list[0].ID)

	exec.mu.Lock()
	assert.Equal(t, 1, exec.callCount, "restart must not re-execute completed work")
	exec.mu.Unlock()
}

// TestHandlerDurableCrashResumeViaSweeper is the crash-recovery contract: a
// task claimed by a worker that died (its lease expires, nothing renews it)
// is reclaimed by the sweeper and re-executed to completion by a live
// handler. Restart recovery and crash recovery are the same mechanism.
func TestHandlerDurableCrashResumeViaSweeper(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "tasks.db")

	// The "crashed" process: claims the task, then never renews or finishes.
	dead := mustTaskStore(t, dsn)
	require.NoError(t, dead.Insert(ctx, &TaskRecord{
		ID: "crashed-task", AgentName: "crash-test", Prompt: "survive me",
		Status: TaskStatusPending, SubmittedAt: time.Now().UTC(),
	}))
	claimed, err := dead.Claim(ctx, "crashed-task", "dead-worker", 150*time.Millisecond)
	require.NoError(t, err)
	require.True(t, claimed)

	// The survivor: same database, fresh handler, sweeper running.
	exec := &mockExecutor{result: "recovered"}
	store := mustTaskStore(t, dsn)
	h := newHandler(t, exec, nil, HandlerConfig{
		Store:         store,
		LeaseTTL:      150 * time.Millisecond,
		SweepInterval: 25 * time.Millisecond,
	})
	stop := h.StartLeaseSweeper(ctx)
	defer stop()

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask("crashed-task")
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond,
		"the sweeper must reclaim the dead worker's lease and re-run the task")

	rec, _ := h.GetTask("crashed-task")
	assert.Equal(t, "recovered", rec.Result)
	exec.mu.Lock()
	assert.Equal(t, 1, exec.callCount, "exactly one live execution — the dead claim never ran")
	exec.mu.Unlock()
}

// blockingExecutor blocks until its context is canceled, recording that the
// cancelation was observed. It is the probe for the lease-loss signal.
type blockingExecutor struct {
	started    chan struct{}
	canceled   chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
}

func (e *blockingExecutor) ExecuteTask(ctx context.Context, _, _ string) (string, error) {
	e.startOnce.Do(func() { close(e.started) })
	<-ctx.Done()
	e.cancelOnce.Do(func() { close(e.canceled) })
	return "", ctx.Err()
}

// TestHandlerDurableLeaseLossCancelsExecutor pins the other half of the
// duplicate-execution hole: when a running worker's lease is taken away, its
// executor context is canceled and it records nothing — the result belongs
// to the new owner.
func TestHandlerDurableLeaseLossCancelsExecutor(t *testing.T) {
	ctx := context.Background()
	store := mustTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	exec := &blockingExecutor{started: make(chan struct{}), canceled: make(chan struct{})}
	h := newHandler(t, exec, nil, HandlerConfig{
		Store:    store,
		LeaseTTL: 300 * time.Millisecond, // renewal ticks at 100ms
	})

	taskID, err := h.Submit(ctx, TaskSubmission{AgentName: "steal-test", Prompt: "block"})
	require.NoError(t, err)

	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor never started")
	}

	// Steal the lease out from under the running worker — what the sweeper
	// plus a new claimer would do after a real expiry.
	_, err = store.db.ExecContext(ctx,
		`UPDATE fleet_tasks SET lease_owner = 'thief', lease_expires_at = ? WHERE id = ?`,
		time.Now().Add(time.Hour).UnixNano(), taskID)
	require.NoError(t, err)

	select {
	case <-exec.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("lease loss did not cancel the executor within the renewal interval")
	}

	// The robbed worker must leave no fingerprints: the task still belongs to
	// the thief and carries no terminal state from this process.
	require.Eventually(t, func() bool {
		rec, ok, err := store.Get(ctx, taskID)
		return err == nil && ok && rec.LeaseOwner == "thief"
	}, 2*time.Second, 10*time.Millisecond)
	rec, _, err := store.Get(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, TaskStatusRunning, rec.Status, "no terminal write from the robbed worker")
	assert.Empty(t, rec.Result)
}

func TestHandlerDurableDuplicateSubmitRejected(t *testing.T) {
	ctx := context.Background()
	store := mustTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	h := newHandler(t, &mockExecutor{result: "ok"}, nil, HandlerConfig{Store: store})

	_, err := h.Submit(ctx, TaskSubmission{TaskID: "idem-1", AgentName: "a", Prompt: "p"})
	require.NoError(t, err)
	_, err = h.Submit(ctx, TaskSubmission{TaskID: "idem-1", AgentName: "a", Prompt: "p"})
	require.Error(t, err, "a caller-supplied task ID is an idempotency key; the durable path must refuse a duplicate")
}

// TestHandlerDurableSweeperDeadLettersAndNotifies: a task whose lease expired
// with its attempt budget spent is dead-lettered by the sweeper and announced
// to completion listeners, so a stalled claim pages instead of sleeping.
func TestHandlerDurableSweeperDeadLettersAndNotifies(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "tasks.db")

	setup := mustTaskStore(t, dsn)
	require.NoError(t, setup.Insert(ctx, &TaskRecord{
		ID: "doomed", AgentName: "dlq-test", Prompt: "p",
		Status: TaskStatusPending, SubmittedAt: time.Now().UTC(),
	}))
	claimed, err := setup.Claim(ctx, "doomed", "dead-worker", 50*time.Millisecond)
	require.NoError(t, err)
	require.True(t, claimed)
	for i := 0; i < 3; i++ { // spend the whole budget (MaxRetries 2 → 3 attempts)
		_, err := setup.BumpAttempt(ctx, "doomed", "dead-worker")
		require.NoError(t, err)
	}

	store := mustTaskStore(t, dsn)
	h := newHandler(t, &mockExecutor{result: "never"}, nil, HandlerConfig{
		Store:         store,
		LeaseTTL:      50 * time.Millisecond,
		SweepInterval: 25 * time.Millisecond,
	})
	var mu sync.Mutex
	var seen []TaskRecord
	h.OnTaskComplete(func(rec TaskRecord) {
		mu.Lock()
		seen = append(seen, rec)
		mu.Unlock()
	})
	stop := h.StartLeaseSweeper(ctx)
	defer stop()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 1
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "doomed", seen[0].ID)
	assert.Equal(t, TaskStatusFailed, seen[0].Status)
	assert.Contains(t, seen[0].Error, "attempt budget spent")
	mu.Unlock()

	rec, _ := h.GetTask("doomed")
	assert.Equal(t, TaskStatusFailed, rec.Status)
}
