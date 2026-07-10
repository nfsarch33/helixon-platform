package helixon

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"time"
)

// RunWithSignal starts the runtime in the foreground, listens for the given
// OS signals (typically SIGINT/SIGTERM), and on the first signal — or parent
// context cancellation — drives a Shutdown bounded by shutdownBudget.
//
// On a signal-driven exit it returns nil. On a parent ctx cancellation it
// returns context.Canceled. Errors from Run or Shutdown are surfaced as-is.
//
// shutdownBudget defaults to 5 seconds when <=0.
func (r *Runtime) RunWithSignal(parent context.Context, shutdownBudget time.Duration, signals ...os.Signal) error {
	if shutdownBudget <= 0 {
		shutdownBudget = 5 * time.Second
	}
	if len(signals) == 0 {
		signals = []os.Signal{os.Interrupt}
	}

	runCtx, cancelRun := context.WithCancel(parent)
	defer cancelRun()

	sigCtx, stop := signal.NotifyContext(parent, signals...)
	defer stop()

	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(runCtx) }()

	var ranErr error
	parentCancelled := false
	select {
	case ranErr = <-runErr:
		// Run returned on its own (channel error or all channels exited).
	case <-sigCtx.Done():
		// Got a signal. Stop notifying so the next signal kills the
		// process for an operator forcing exit.
		stop()
		cancelRun()
	case <-parent.Done():
		parentCancelled = true
		cancelRun()
	}

	// Wait for Run goroutine to finish if we triggered cancellation.
	if ranErr == nil {
		select {
		case ranErr = <-runErr:
		case <-time.After(shutdownBudget):
			// Don't block forever; Shutdown below will be capped too.
		}
	}

	//nolint:contextcheck // intentional detach; parent ctx is cancelled by Run's return
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancelShutdown()

	shutErr := r.Shutdown(shutdownCtx) //nolint:contextcheck
	switch {
	case ranErr != nil && !errors.Is(ranErr, context.Canceled):
		return ranErr
	case shutErr != nil:
		return shutErr
	case parentCancelled:
		return context.Canceled
	default:
		return nil
	}
}
