package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One full-blown scenario this pins: a run-store renew hit
// "context deadline exceeded" (slow fsync, two concurrent runs on one
// SQLite), the renewer canceled the healthy run, the run was left resumable
// - and the ticket was escalated anyway, so the resumable run was never
// resumed. The renewer must classify:
// a transient store error is retried and only ends the run when enough
// consecutive ticks have failed that the lease would actually lapse; a
// definitive refusal (another owner) cancels immediately; and a
// lease-canceled run surfaces ErrLeaseLost so the poller abandons the ticket
// instead of escalating it.

// TestRenewAttemptTimeoutBoundsTickInsideTTLThird pins the per-attempt
// bound: three attempts plus backoff must fit inside a renewal tick (a
// third of the TTL), while long leases keep the 5s ceiling.
func TestRenewAttemptTimeoutBoundsTickInsideTTLThird(t *testing.T) {
	assert.Equal(t, 2500*time.Millisecond, renewAttemptTimeout(30*time.Second))
	assert.Equal(t, 5*time.Second, renewAttemptTimeout(time.Minute))
	assert.Equal(t, 5*time.Second, renewAttemptTimeout(0))
	assert.Equal(t, 250*time.Millisecond, renewAttemptTimeout(3*time.Second))
}

// faultRenewStore stages RenewRun outcomes in order; once the script is
// spent it repeats the last outcome.
type faultRenewStore struct {
	calls int64
	seq   []renewOutcome
}

type renewOutcome struct {
	ok  bool
	err error
}

func (f *faultRenewStore) RenewRun(_ context.Context, _, _ string, _ time.Duration) (bool, error) {
	i := int(atomic.AddInt64(&f.calls, 1)) - 1
	if i >= len(f.seq) {
		i = len(f.seq) - 1
	}
	out := f.seq[i]
	return out.ok, out.err
}

var errDeadline = fmt.Errorf("renew run: %w", context.DeadlineExceeded)

func TestRenewTransientTimeoutIsRetriedNotCanceled(t *testing.T) {
	a, _, _ := newDurableAgent(t, &mockProvider{}, &countingExecutor{})
	a.renewals = &faultRenewStore{seq: []renewOutcome{
		{false, errDeadline}, // the blip
		{true, nil},          // the retry inside the same tick succeeds
	}}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &renewState{}

	assert.True(t, a.renewOnce(runCtx, cancel, "run-1", state), "a single transient timeout must not end the run")
	assert.NoError(t, runCtx.Err(), "the run context must survive one transient renew timeout")
}

func TestRenewTransientStreakCancelsAtTTLBoundary(t *testing.T) {
	a, _, _ := newDurableAgent(t, &mockProvider{}, &countingExecutor{})
	a.renewals = &faultRenewStore{seq: []renewOutcome{{false, errDeadline}}}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &renewState{}

	// Tick 1 misses: the lease (set by the last successful renewal) still
	// spans the next tick, so the run keeps going.
	assert.True(t, a.renewOnce(runCtx, cancel, "run-1", state))
	assert.NoError(t, runCtx.Err())

	// Tick 2 misses: the remaining lease cannot span another tick - by the
	// time tick 3 would fire, the lease would have lapsed.
	assert.False(t, a.renewOnce(runCtx, cancel, "run-1", state))
	assert.ErrorIs(t, runCtx.Err(), context.Canceled)
	assert.True(t, state.lost.Load(), "a TTL-spanning streak is recorded as lease loss")
}

func TestRenewLeaseLostCancelsImmediately(t *testing.T) {
	a, _, _ := newDurableAgent(t, &mockProvider{}, &countingExecutor{})
	a.renewals = &faultRenewStore{seq: []renewOutcome{{false, nil}}}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &renewState{}

	assert.False(t, a.renewOnce(runCtx, cancel, "run-1", state), "a definitive refusal cancels on the first tick")
	assert.ErrorIs(t, runCtx.Err(), context.Canceled)
	assert.True(t, state.lost.Load())
}

func TestRenewTransientThenSuccessResetsTheStreak(t *testing.T) {
	a, _, _ := newDurableAgent(t, &mockProvider{}, &countingExecutor{})
	// Each tick makes up to three attempts; the script is consumed across
	// attempts, and the last outcome repeats once the script is spent.
	a.renewals = &faultRenewStore{seq: []renewOutcome{
		{false, errDeadline}, {false, errDeadline}, {false, errDeadline}, // tick 1: all attempts fail -> streak 1
		{true, nil},                                                      // tick 2: renews -> streak reset
		{false, errDeadline}, {false, errDeadline}, {false, errDeadline}, // tick 3: fully fails ONCE = fresh streak 1
	}}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &renewState{}

	assert.True(t, a.renewOnce(runCtx, cancel, "run-1", state))
	assert.True(t, a.renewOnce(runCtx, cancel, "run-1", state))
	// A streak that restarted at the last renewal needs TWO fully-failed
	// ticks before the TTL-lapse cancel; one is not a lapse.
	assert.True(t, a.renewOnce(runCtx, cancel, "run-1", state))
	assert.NoError(t, runCtx.Err())
}

// TestRunDurable_LeaseLostSurfacesTypedErrorAndStaysResumable pins the
// poller-facing half: a run canceled by the lease policy returns an error
// carrying ErrLeaseLost and stays RunRunning, so the recovery sweep - not an
// escalation - picks it up.
func TestRunDurable_LeaseLostSurfacesTypedErrorAndStaysResumable(t *testing.T) {
	store, _ := newClockedStore(t, time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	cfg := Config{
		MaxIterations: 3, MaxTokens: 1000, Timeout: 10 * time.Second, LeaseTTL: 90 * time.Millisecond,
		Completion: CompletionPolicy{Enabled: false},
	}
	a := New(slowProvider{}, &countingExecutor{}, store, cfg)
	a.renewals = &faultRenewStore{seq: []renewOutcome{{false, nil}}}
	sid := newRunSession(t, store)

	_, err := a.RunDurable(context.Background(), "run-lease", sid, "q", nil)
	require.ErrorIs(t, err, ErrLeaseLost, "a lease-canceled attempt must be recognizable by the caller")

	run, err := store.GetRun(context.Background(), "run-lease")
	require.NoError(t, err)
	assert.Equal(t, RunRunning, run.Status, "the run stays resumable for the recovery sweep")
}
