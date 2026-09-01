package fleet

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeClock is a caller-driven wall clock, so lease expiry is tested by
// moving time, never by sleeping toward a real deadline.
type storeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *storeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *storeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestTaskStore(t *testing.T, dsn string) (*TaskStore, *storeClock) {
	t.Helper()
	store, err := NewTaskStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	clock := &storeClock{t: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}
	store.now = clock.Now
	return store, clock
}

func pendingTask(id string) *TaskRecord {
	return &TaskRecord{
		ID:          id,
		AgentName:   "store-test",
		Prompt:      "do the thing",
		Status:      TaskStatusPending,
		SubmittedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		Metadata:    map[string]any{"k": "v"},
		TimeoutSecs: 30,
	}
}

func TestTaskStoreRoundtripAndReopen(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "tasks.db")
	store, _ := newTestTaskStore(t, dsn)

	require.NoError(t, store.Insert(ctx, pendingTask("t1")))

	rec, ok, err := store.Get(ctx, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, TaskStatusPending, rec.Status)
	assert.Equal(t, "do the thing", rec.Prompt)
	assert.Equal(t, 30, rec.TimeoutSecs)
	assert.Equal(t, map[string]any{"k": "v"}, rec.Metadata)

	ok2, err := store.Claim(ctx, "t1", "w1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok2)
	wrote, err := store.Finish(ctx, "t1", "w1", TaskStatusCompleted, "the result", "")
	require.NoError(t, err)
	require.True(t, wrote)

	// A completed result must survive the process: reopen the same file and
	// read it back — this is the property the in-memory map never had.
	require.NoError(t, store.Close())
	reopened, err := NewTaskStore(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	rec, ok, err = reopened.Get(ctx, "t1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, TaskStatusCompleted, rec.Status)
	assert.Equal(t, "the result", rec.Result)
	assert.Empty(t, rec.LeaseOwner, "finish must clear the lease")
	assert.Nil(t, rec.LeaseExpiresAt)

	_, ok, err = reopened.Get(ctx, "absent")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestTaskStoreInsertDuplicateIDRejected(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, store.Insert(ctx, pendingTask("dup")))
	err := store.Insert(ctx, pendingTask("dup"))
	require.Error(t, err, "a task ID is an idempotency key; resubmission must be refused, not overwritten")
}

// TestTaskStoreClaimExactlyOneWinner is the positive control for the CAS the
// whole ownership model rests on: many claimers, two independent store
// handles on one database file (two "processes"), exactly one winner.
func TestTaskStoreClaimExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "tasks.db")
	a, _ := newTestTaskStore(t, dsn)
	b, _ := newTestTaskStore(t, dsn)

	require.NoError(t, a.Insert(ctx, pendingTask("contested")))

	const claimers = 32
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < claimers; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		owner := fmt.Sprintf("owner-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Claim(ctx, "contested", owner, time.Minute)
			assert.NoError(t, err)
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load(), "exactly one of %d concurrent claimers may win", claimers)
}

func TestTaskStoreClaimOnlyPending(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, store.Insert(ctx, pendingTask("t1")))

	ok, err := store.Claim(ctx, "t1", "w1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Already claimed: refused, even for the same owner — claims are not
	// re-entrant, renewal is the way to keep one.
	ok, err = store.Claim(ctx, "t1", "w2", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = store.Claim(ctx, "t1", "w1", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)

	// Terminal: refused.
	_, err = store.Finish(ctx, "t1", "w1", TaskStatusCompleted, "r", "")
	require.NoError(t, err)
	ok, err = store.Claim(ctx, "t1", "w3", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestTaskStoreRenewOwnerGuard(t *testing.T) {
	ctx := context.Background()
	store, clock := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, store.Insert(ctx, pendingTask("t1")))
	ok, err := store.Claim(ctx, "t1", "w1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	before, _, err := store.Get(ctx, "t1")
	require.NoError(t, err)
	require.NotNil(t, before.LeaseExpiresAt)

	clock.Advance(30 * time.Second)
	ok, err = store.Renew(ctx, "t1", "wrong-owner", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "renewal by a non-owner must be refused")

	ok, err = store.Renew(ctx, "t1", "w1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	after, _, err := store.Get(ctx, "t1")
	require.NoError(t, err)
	assert.True(t, after.LeaseExpiresAt.After(*before.LeaseExpiresAt),
		"owner renewal must extend the lease deadline")
}

func TestTaskStoreBumpAttemptRequiresLease(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, store.Insert(ctx, pendingTask("t1")))

	_, err := store.BumpAttempt(ctx, "t1", "nobody")
	require.ErrorIs(t, err, ErrLeaseLost)

	ok, err := store.Claim(ctx, "t1", "w1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	n, err := store.BumpAttempt(ctx, "t1", "w1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = store.BumpAttempt(ctx, "t1", "w1")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

// TestTaskStoreFinishRefusedAfterReclaim closes the duplicate-execution hole:
// a worker that lost its lease must not be able to record a result over the
// new owner's.
func TestTaskStoreFinishRefusedAfterReclaim(t *testing.T) {
	ctx := context.Background()
	store, clock := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, store.Insert(ctx, pendingTask("t1")))

	ok, err := store.Claim(ctx, "t1", "dead-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	clock.Advance(2 * time.Minute)
	requeued, dead, err := store.ReclaimExpired(ctx, 3)
	require.NoError(t, err)
	require.Empty(t, dead)
	require.Len(t, requeued, 1)
	assert.Equal(t, TaskStatusPending, requeued[0].Status)

	ok, err = store.Claim(ctx, "t1", "new-worker", time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "a reclaimed task must be claimable by a new worker")

	wrote, err := store.Finish(ctx, "t1", "dead-worker", TaskStatusCompleted, "zombie result", "")
	require.NoError(t, err)
	assert.False(t, wrote, "the dead worker's finish must be refused")

	rec, _, err := store.Get(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, "new-worker", rec.LeaseOwner)
	assert.NotEqual(t, "zombie result", rec.Result)
}

func TestTaskStoreReclaimDeadLettersSpentAttempts(t *testing.T) {
	ctx := context.Background()
	store, clock := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))

	// spent: at the attempt budget; fresh-budget: one attempt in.
	require.NoError(t, store.Insert(ctx, pendingTask("spent")))
	require.NoError(t, store.Insert(ctx, pendingTask("fresh-budget")))
	for _, id := range []string{"spent", "fresh-budget"} {
		ok, err := store.Claim(ctx, id, "w1", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
	}
	for i := 0; i < 3; i++ {
		_, err := store.BumpAttempt(ctx, "spent", "w1")
		require.NoError(t, err)
	}
	_, err := store.BumpAttempt(ctx, "fresh-budget", "w1")
	require.NoError(t, err)

	clock.Advance(2 * time.Minute)
	requeued, dead, err := store.ReclaimExpired(ctx, 3)
	require.NoError(t, err)

	require.Len(t, dead, 1)
	assert.Equal(t, "spent", dead[0].ID)
	assert.Equal(t, TaskStatusFailed, dead[0].Status)
	assert.Contains(t, dead[0].Error, "attempt budget spent")

	require.Len(t, requeued, 1)
	assert.Equal(t, "fresh-budget", requeued[0].ID)
	assert.Equal(t, TaskStatusPending, requeued[0].Status)

	// The dead letter is terminal in the store too, not just in the return.
	rec, _, err := store.Get(ctx, "spent")
	require.NoError(t, err)
	assert.Equal(t, TaskStatusFailed, rec.Status)
	require.NotNil(t, rec.CompletedAt)
}

func TestTaskStoreReclaimLeavesFreshLeases(t *testing.T) {
	ctx := context.Background()
	store, clock := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, store.Insert(ctx, pendingTask("expired")))
	require.NoError(t, store.Insert(ctx, pendingTask("alive")))

	ok, err := store.Claim(ctx, "expired", "w1", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	clock.Advance(2 * time.Minute)
	// "alive" is claimed AFTER the advance, so its lease is fresh.
	ok, err = store.Claim(ctx, "alive", "w2", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	requeued, dead, err := store.ReclaimExpired(ctx, 3)
	require.NoError(t, err)
	require.Empty(t, dead)
	require.Len(t, requeued, 1)
	assert.Equal(t, "expired", requeued[0].ID)

	rec, _, err := store.Get(ctx, "alive")
	require.NoError(t, err)
	assert.Equal(t, "w2", rec.LeaseOwner, "a live lease must never be reclaimed")
	assert.Equal(t, TaskStatusClaimed, rec.Status)
}

func TestTaskStoreListNewestFirst(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestTaskStore(t, filepath.Join(t.TempDir(), "tasks.db"))
	for _, id := range []string{"first", "second", "third"} {
		require.NoError(t, store.Insert(ctx, pendingTask(id)))
	}
	out, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, "third", out[0].ID)
	assert.Equal(t, "first", out[2].ID)
}
