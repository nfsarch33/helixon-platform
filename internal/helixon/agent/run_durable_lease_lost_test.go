package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
)

// A run whose context is cancelled BECAUSE the claim was lost (the cause carries
// controlplane.ErrTicketNotClaimedBy) must end in RunFailed and must never be
// listed as interrupted: the start-up recovery sweep would otherwise resume it
// and complete a ticket another holder now owns. A plain cancellation keeps the
// run resumable (see TestRunDurable_CancellationLeavesTheRunResumable).
func TestRunDurable_LeaseLostCauseTerminalisesTheRun(t *testing.T) {
	store, clock := newClockedStore(t, time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC))
	cfg := Config{MaxIterations: 3, MaxTokens: 1000, Timeout: 10 * time.Second, Completion: CompletionPolicy{Enabled: false}}
	a := New(slowProvider{}, &countingExecutor{}, store, cfg)
	sid := newRunSession(t, store)

	deadlineCtx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	ctx, cancel := context.WithCancelCause(deadlineCtx)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel(controlplane.ErrTicketNotClaimedBy)
	}()

	_, err := a.RunDurable(ctx, "run-lease-lost", sid, "q", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, controlplane.ErrTicketNotClaimedBy, "the error carries the lease-lost cause")

	run, gerr := store.GetRun(context.Background(), "run-lease-lost")
	require.NoError(t, gerr)
	assert.Equal(t, RunFailed, run.Status, "a lease-lost cancel terminalises the run")

	clock.Add(time.Minute)
	interrupted, ierr := store.ListInterruptedRuns(context.Background())
	require.NoError(t, ierr)
	assert.Empty(t, interrupted, "a terminalised run is not resumable")
}
