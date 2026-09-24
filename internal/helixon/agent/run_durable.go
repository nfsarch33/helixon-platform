package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/leaselost"
	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// Durable execution of the agent loop.
//
// RunDurable and Resume are the two entry points; Run is RunDurable with a
// fresh id. A run is claimed with a TTL lease before any model call, renewed
// from a goroutine at a third of the TTL, and finished under the owner guard.
// Losing the lease cancels the run's context, so a worker that was presumed
// dead cannot write a result over the worker that took over.
//
// Resume rebuilds "where were we" from the durable log alone: the last
// assistant turn's tool calls, which of them have a tool turn (done), and
// which have a pending step with no tool turn (dispatched, outcome unknown).
// An unknown-outcome call to a non-mutating tool is re-executed; one to a
// mutating tool is never re-executed - the run stops for a human, because a
// second `shell` or `file_write` is precisely the duplicated side effect the
// soak exists to prove impossible.

var (
	// ErrLeaseHeld is returned when another worker holds the run's lease.
	ErrLeaseHeld = errors.New("agent: run lease held by another worker")
	// ErrLeaseLost is returned when this worker's lease lapsed mid-run and was
	// taken over, so the result it computed was refused. It is produced in two
	// places: a refused finish write (another owner already moved the run on),
	// and a renewal verdict that canceled the loop (the run-store lease was
	// reclaimed, or enough consecutive renewal ticks failed that the lease had
	// effectively lapsed). Both mean the same thing to a caller: this attempt
	// is over, the run is resumable, and reporting it as a failure would
	// strand work the recovery sweep should finish.
	ErrLeaseLost = errors.New("agent: run lease lost during execution")
	// ErrRunFinished wraps the stored error of a run that already ended.
	ErrRunFinished = errors.New("agent: run already finished")
	// ErrInterruptedMutation stops a resumed run whose last mutating tool call
	// was dispatched but never recorded an outcome.
	ErrInterruptedMutation = errors.New("agent: a mutating tool call was interrupted before its outcome was recorded; stopping for human approval")
)

const defaultLeaseTTL = 30 * time.Second

// RunDurable starts (or re-finds) the run with the given id and executes it
// to a terminal state, or returns the stored result if it already ended.
// meta is stored with the run (a ticket id, a channel) so a recovery sweep
// knows what the run was for.
func (a *Agent) RunDurable(ctx context.Context, runID, sessionID, userMessage string, meta map[string]string) (*RunResult, error) {
	if runID == "" {
		return nil, errors.New("agent: run id is required")
	}
	// The row is born claimed by this owner: a run that existed unclaimed for
	// even a moment could be taken by a recovery sweep listing never-claimed
	// runs, and the live caller would be refused its own run.
	run, created, err := a.store.StartRunClaimed(ctx, runID, sessionID, userMessage, meta, a.owner, a.cfg.LeaseTTL)
	if err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}
	return a.execute(ctx, run, created)
}

// Resume continues a run from its durable state. A run that already ended
// returns its stored result with ErrRunFinished whatever its status, so a
// recovery sweep can tell "I finished it" from "it was already finished"
// and not report the latter twice.
func (a *Agent) Resume(ctx context.Context, runID string) (*RunResult, error) {
	run, err := a.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != RunRunning {
		res, serr := storedResult(run)
		if serr == nil {
			serr = fmt.Errorf("%w: %s", ErrRunFinished, run.Status)
		}
		return res, serr
	}
	return a.execute(ctx, run, false)
}

// execute claims the run (unless the caller created it claimed), replays
// where it was, drives the loop, and finishes.
func (a *Agent) execute(ctx context.Context, run *RunRecord, claimed bool) (*RunResult, error) {
	if run.Status != RunRunning {
		return storedResult(run)
	}
	if !a.markActive(run.ID) {
		return nil, ErrLeaseHeld
	}
	defer a.unmarkActive(run.ID)
	if !claimed {
		ok, err := a.store.ClaimRun(ctx, run.ID, a.owner, a.cfg.LeaseTTL)
		if err != nil {
			return nil, fmt.Errorf("claim run: %w", err)
		}
		if !ok {
			// Either another worker holds it, or it ended between GetRun and here.
			if fresh, gerr := a.store.GetRun(ctx, run.ID); gerr == nil && fresh.Status != RunRunning {
				return storedResult(fresh)
			}
			return nil, ErrLeaseHeld
		}
	}

	// v18846-3: the run's identity rides the context into every model call,
	// where the identity doer stamps it as headers (X-HLXN-Run-Id et al).
	// Attached here so RunDurable, Resume, and the recovery sweep - every
	// path through execute - carry it.
	ctx = llm.WithRunInfo(ctx, llm.RunInfo{
		RunID:    run.ID,
		TicketID: run.Meta["ticket_id"],
		AgentID:  a.owner,
	})
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()
	stopRenew, renew := a.startRenewal(ctx, cancel, run.ID)
	defer stopRenew()

	result := &RunResult{SessionID: run.SessionID}
	loopErr := a.resumeLoop(ctx, run, result)
	return a.finish(ctx, run, result, loopErr, renew)
}

// finish maps the loop's outcome to a terminal status and records it under
// the owner guard. Cancellation and deadline are different verdicts:
//
//   - canceled (a shutdown, or the lease was lost to another worker): the
//     attempt is over, the run is not. It is left running and its lease
//     lapses into the recovery sweep's hands.
//   - deadline (the run's own budget, or the caller's): the run FAILED. A
//     restart must not hand a run that exhausted its budget a fresh one, and
//     the ticket that already got the timeout escalated must not be reported
//     a second time by the sweep.
func (a *Agent) finish(ctx context.Context, run *RunRecord, result *RunResult, loopErr error, renew *renewState) (*RunResult, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		if cause := context.Cause(ctx); errors.Is(cause, controlplane.ErrTicketNotClaimedBy) {
			// A board-lease cancellation is TERMINAL, not resumable. The poller's
			// renewal loop cancels the run context WITH
			// controlplane.ErrTicketNotClaimedBy as the cause; a resumable run here
			// would let the recovery sweep resume it and complete a ticket this
			// agent no longer owns on the board.
			if loopErr == nil {
				loopErr = context.Canceled
			}
			loopErr = fmt.Errorf("%w: %w", controlplane.ErrTicketNotClaimedBy, loopErr)
			a.logger.Warn("run attempt canceled: board lease lost; failing the run so recovery cannot resume it",
				slog.String("run_id", run.ID))
			// Fall through to the owner-guarded finish write: RunFailed.
		} else if renew != nil && renew.lost.Load() {
			// The renewer canceled this attempt because the RUN-STORE lease is
			// gone (typed 409, not a transient store error — renewOnce only
			// raises this flag on the definitive refusal). Tag it so the caller
			// (the ticket poller) can abandon the ticket instead of escalating:
			// the run is resumable and the recovery sweep, not a human, owns
			// finishing it.
			if loopErr == nil {
				loopErr = ctx.Err()
			}
			loopErr = errors.Join(ErrLeaseLost, loopErr)
			a.logger.Warn("run attempt canceled: lease lost; leaving the run resumable", slog.String("run_id", run.ID))
			return result, loopErr
		} else {
			a.logger.Warn("run attempt canceled; leaving the run resumable", slog.String("run_id", run.ID))
			if loopErr == nil {
				loopErr = ctx.Err()
			}
			return result, loopErr
		}
	}
	if ctx.Err() != nil && loopErr == nil {
		loopErr = ErrTimeout
	}
	if loopErr != nil && result.Err == nil {
		result.Err = loopErr
	}
	status := RunCompleted
	switch {
	case loopErr == nil:
	case result.NeedsHumanApproval:
		status = RunNeedsHuman
	default:
		status = RunFailed
	}
	// The finish write uses a detached context: the loop's context is alive
	// here, but a terminal verdict must not be lost to a deadline that fires
	// between the last tool call and this write.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	ok, err := a.store.FinishRun(finishCtx, run.ID, a.owner, status, result, loopErr)
	if err != nil {
		return result, fmt.Errorf("finish run: %w", err)
	}
	if !ok {
		return result, ErrLeaseLost
	}
	return result, loopErr
}

// storedResult reconstructs the RunResult of a run that already ended.
func storedResult(run *RunRecord) (*RunResult, error) {
	res := &RunResult{
		SessionID: run.SessionID, FinalContent: run.FinalContent, Iterations: run.Iterations,
		TokensIn: run.TokensIn, TokensOut: run.TokensOut,
		NeedsHumanApproval: run.Status == RunNeedsHuman,
	}
	if run.Status == RunCompleted {
		return res, nil
	}
	err := fmt.Errorf("%w: %s: %s", ErrRunFinished, run.Status, run.Err)
	res.Err = err
	return res, err
}

// resumeLoop replays the durable state and drives the loop to a terminal
// decision. It returns the loop's error (nil on a final answer).
func (a *Agent) resumeLoop(ctx context.Context, run *RunRecord, result *RunResult) error {
	turns, err := a.store.ListTurns(ctx, run.SessionID, 0)
	if err != nil {
		return storeErr(ctx, "list turns", err)
	}
	last, iterations := lastAssistantTurn(turns)
	result.Iterations = iterations
	if last != nil {
		if len(last.ToolCalls) == 0 {
			// The final answer was written; only the run's closing was lost.
			_, err := finalizeRun(result, last.Content, 0)
			if err != nil {
				return err
			}
			return a.gateCompletion(result)
		}
		var calls []llm.ToolCall
		if err := json.Unmarshal(last.ToolCalls, &calls); err != nil {
			return fmt.Errorf("resume: decode tool calls: %w", err)
		}
		if err := a.executeToolCalls(ctx, run.ID, run.SessionID, iterations, last.Seq, calls, result); err != nil {
			return err
		}
	}
	for iter := result.Iterations; iter < a.cfg.MaxIterations; iter++ {
		result.Iterations = iter + 1
		final, err := a.iterateRun(ctx, run.ID, run.SessionID, iter+1, run.UserMessage, result)
		if err != nil {
			return err
		}
		if final {
			return nil
		}
	}
	result.Err = ErrMaxIterations
	return ErrMaxIterations
}

// lastAssistantTurn returns the most recent assistant turn and how many
// assistant turns the session holds (the iterations already paid for).
func lastAssistantTurn(turns []Turn) (*Turn, int) {
	var last *Turn
	count := 0
	for i := range turns {
		if turns[i].Role == RoleAssistant {
			last = &turns[i]
			count++
		}
	}
	return last, count
}

// renewState is one run's renewal-loop bookkeeping: whether the loop canceled
// the run because the lease is gone, and the consecutive-failed-ticks streak
// that decides when a transient problem has spanned the lease TTL.
type renewState struct {
	lost atomic.Bool
	tr   leaselost.Tracker
}

// renewMissesBudget is how many consecutive renewal ticks may fully fail
// (after in-tick retries) before the run is canceled. Ticks fire at a third
// of the TTL: after the second missed tick the remaining lease cannot span
// the next one, so a third tick would fire on an expired lease - that is the
// "TTL would actually lapse" boundary (v18860-1 run-lease-transient-timeout).
const renewMissesBudget = 2

// startRenewal renews the lease at a third of the TTL until stop is called.
// The goroutine is joined by stop, so the package's goleak check sees it end.
func (a *Agent) startRenewal(ctx context.Context, cancel context.CancelFunc, runID string) (func(), *renewState) {
	done := make(chan struct{})
	finished := make(chan struct{})
	state := &renewState{}
	go func() {
		defer close(finished)
		ticker := time.NewTicker(a.cfg.LeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !a.renewOnce(ctx, cancel, runID, state) {
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}, state
}

// renewOnce extends the lease for one renewal tick. Errors are not verdicts:
// a transient store failure (deadline, SQLITE_BUSY - concurrent runs share
// one SQLite file and this host's fsync is slow) is retried inside the tick,
// and only ends the run when enough consecutive ticks have failed that the
// lease would actually lapse. A definitive refusal (another owner holds the
// run) cancels immediately. Exposed at package level for the contract tests.
func (a *Agent) renewOnce(ctx context.Context, cancel context.CancelFunc, runID string, state *renewState) bool {
	const attempts = 3
	var (
		ok  bool
		err error
	)
	for i := 0; i < attempts; i++ {
		renewCtx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		ok, err = a.renewals.RenewRun(renewCtx, runID, a.owner, a.cfg.LeaseTTL)
		rcancel()
		if err == nil || i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			// The run itself is ending; nothing left to renew for.
			return true
		case <-time.After(time.Duration(i+1) * 250 * time.Millisecond):
		}
	}
	switch {
	case err == nil && ok:
		state.tr.Record(leaselost.Renewed)
		return true
	case err == nil:
		state.lost.Store(true)
		state.tr.Record(leaselost.LeaseLost)
		a.logger.Warn("lease lost; canceling the run", slog.String("run_id", runID))
		cancel()
		return false
	default:
		streak := state.tr.Record(leaselost.Retry)
		if state.tr.Exhausted(renewMissesBudget) {
			state.lost.Store(true)
			a.logger.Warn("lease renewal failed on consecutive ticks spanning the TTL; canceling the run",
				slog.String("run_id", runID), slog.Int("missed_ticks", streak))
			cancel()
			return false
		}
		a.logger.Warn("lease renewal failed (transient); run continues, retrying next tick",
			slog.String("run_id", runID), slog.String("error", err.Error()))
		return true
	}
}

// markActive registers a run as executing in this process; false when it
// already is.
func (a *Agent) markActive(runID string) bool {
	a.activeMu.Lock()
	defer a.activeMu.Unlock()
	if _, busy := a.active[runID]; busy {
		return false
	}
	a.active[runID] = struct{}{}
	return true
}

func (a *Agent) unmarkActive(runID string) {
	a.activeMu.Lock()
	delete(a.active, runID)
	a.activeMu.Unlock()
}

// newOwner identifies this Agent instance as a lease owner.
func newOwner() string { return uuid.New().String() }

// NewRunID mints a run id for callers that want to name a run up front.
func NewRunID() string { return uuid.New().String() }

// MaxRunAttempts is the attempt budget a recovery sweep dead-letters at.
func (a *Agent) MaxRunAttempts() int { return a.cfg.MaxRunAttempts }
