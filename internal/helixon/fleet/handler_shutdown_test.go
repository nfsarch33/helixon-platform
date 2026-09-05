package fleet

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// gateHold is how long the gated executor holds a task goroutine open. It is
// deliberately longer than goleak's own settling budget (~430ms of backoff
// across its default 20 retries), so a handler that does not join its task
// goroutines is caught by goleak.Find here rather than left to surface as an
// intermittent whole-package failure later.
const gateHold = 750 * time.Millisecond

// gatedExecutor blocks inside ExecuteTask until released or canceled, so a
// test can hold a task goroutine at a known point and watch what the handler
// does around it.
type gatedExecutor struct {
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
}

func (e *gatedExecutor) ExecuteTask(ctx context.Context, _, _ string) (string, error) {
	e.enterOnce.Do(func() { close(e.entered) })
	select {
	case <-e.release:
		return "gated result", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestHandlerShutdownJoinsSubmittedTaskGoroutines is the regression test for
// the intermittent CI failure this shutdown path was added for: a goroutine
// Submit had started was still inside TaskStore.Claim when the test that
// submitted it returned and its cleanup closed the database, so database/sql
// handed that goroutine the connection teardown — a WAL checkpoint and an
// fsync — and goleak found it still running at process exit.
//
// The contract that kills the whole class: when Shutdown returns, nothing the
// handler started is still using the store.
func TestHandlerShutdownJoinsSubmittedTaskGoroutines(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	ctx := context.Background()
	store := mustTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))

	exec := &gatedExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	// Not newHandler: this test drives the shutdown itself.
	h := NewHandler(exec, nil, HandlerConfig{Store: store})

	taskID, err := h.Submit(ctx, TaskSubmission{AgentName: "shutdown-test", Prompt: "hold"})
	require.NoError(t, err)

	select {
	case <-exec.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the submitted task never reached the executor")
	}

	// Release the task only after a delay. A Shutdown that genuinely joins
	// cannot return before this fires; one that does not will return with the
	// task still mid-flight and the assertions below will say so.
	go func() {
		time.Sleep(gateHold)
		close(exec.release)
	}()

	start := time.Now()
	require.NoError(t, h.Shutdown(ctx))
	assert.GreaterOrEqual(t, time.Since(start), gateHold,
		"Shutdown returned while the task was still running")

	// The task's terminal write has landed, which is only true if Shutdown
	// waited for the goroutine to finish rather than for the executor alone.
	rec, ok, err := store.Get(ctx, taskID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, TaskStatusCompleted, rec.Status)
	assert.Equal(t, "gated result", rec.Result)

	// And nothing is left running to inherit the connection teardown when the
	// store's owner closes it.
	require.NoError(t, goleak.Find(ignore))
	require.NoError(t, store.Close())
}

// TestHandlerShutdownDeadlineCancelsButStillJoins pins the other half: a
// deadline converts a graceful drain into a cancelation, and Shutdown still
// does not return until the tasks it canceled have unwound. Returning early
// is what would put a live goroutine on a closing database.
func TestHandlerShutdownDeadlineCancelsButStillJoins(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	ctx := context.Background()
	store := mustTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))

	exec := &blockingExecutor{started: make(chan struct{}), canceled: make(chan struct{})}
	h := NewHandler(exec, nil, HandlerConfig{Store: store})

	_, err := h.Submit(ctx, TaskSubmission{AgentName: "deadline-test", Prompt: "block"})
	require.NoError(t, err)

	select {
	case <-exec.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the submitted task never reached the executor")
	}

	stopCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = h.Shutdown(stopCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"an expired deadline must be reported, not swallowed")

	// The executor was canceled, not abandoned, and the goroutine is gone.
	select {
	case <-exec.canceled:
	default:
		t.Fatal("shutdown returned before the executor observed cancelation")
	}
	require.NoError(t, goleak.Find(ignore))

	// A submission after shutdown is refused rather than dispatched into a
	// store the caller is about to close.
	lateID, err := h.Submit(ctx, TaskSubmission{TaskID: "late", AgentName: "late", Prompt: "p"})
	require.ErrorIs(t, err, ErrShuttingDown)
	require.NoError(t, goleak.Find(ignore))

	// The durable row survives as pending: refusing to run it here is the
	// same state a crashed worker leaves, and a sweeper claims it later.
	rec, ok, err := store.Get(ctx, lateID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, TaskStatusPending, rec.Status)

	// Shutdown is idempotent.
	require.NoError(t, h.Shutdown(ctx))
	require.NoError(t, store.Close())
}

// TestHandlerShutdownJoinsSweeperRedispatch covers the second place the
// handler starts goroutines: the lease sweeper requeues an expired task and
// dispatches it locally. Those goroutines use the store exactly like a
// submitted one, so Shutdown has to join them too.
func TestHandlerShutdownJoinsSweeperRedispatch(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "tasks.db")

	dead := mustTaskStore(t, dsn)
	require.NoError(t, dead.Insert(ctx, &TaskRecord{
		ID: "abandoned", AgentName: "sweeper-test", Prompt: "p",
		Status: TaskStatusPending, SubmittedAt: time.Now().UTC(),
	}))
	claimed, err := dead.Claim(ctx, "abandoned", "dead-worker", time.Millisecond)
	require.NoError(t, err)
	require.True(t, claimed)

	exec := &gatedExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	store := mustTaskStore(t, dsn)
	h := NewHandler(exec, nil, HandlerConfig{
		Store:         store,
		LeaseTTL:      2 * time.Second,
		SweepInterval: 10 * time.Millisecond,
	})
	h.StartLeaseSweeper(ctx) // deliberately not stopped: Shutdown must do it

	select {
	case <-exec.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the sweeper never redispatched the reclaimed task")
	}

	go func() {
		time.Sleep(gateHold)
		close(exec.release)
	}()
	start := time.Now()
	require.NoError(t, h.Shutdown(ctx))
	assert.GreaterOrEqual(t, time.Since(start), gateHold,
		"Shutdown returned while a sweeper-dispatched task was still running")

	require.NoError(t, goleak.Find(ignore))
	require.NoError(t, store.Close())
}
