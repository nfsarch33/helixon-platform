package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// This file is the durable execution path the Handler takes when a TaskStore
// is configured. The in-memory path in handler.go is untouched; the two share
// the semaphore, the retry budget, and the listener contract, and differ only
// in where task state lives and in the lease protocol wrapped around the
// executor call.

// processTaskDurable executes one stored task under a lease. The shape is:
// claim (CAS) → renew in the background → run with per-attempt timeout →
// finish (owner-guarded). Losing the lease at any point cancels the executor
// and forfeits the right to write a result.
func (h *Handler) processTaskDurable(ctx context.Context, id string, timeout time.Duration) {
	select {
	case h.sem <- struct{}{}:
	case <-ctx.Done():
		// The task stays pending; the sweeper or the next process picks it up.
		return
	}
	defer func() { <-h.sem }()

	claimed, err := h.store.Claim(ctx, id, h.owner, h.leaseTTL)
	if err != nil {
		h.logger.Warn("durable claim failed",
			slog.String("task_id", id), slog.String("error", err.Error()))
		return
	}
	if !claimed {
		// Another worker won the CAS, or the task is no longer pending. Both
		// are normal outcomes, not errors.
		return
	}

	rec, found, err := h.store.Get(ctx, id)
	if err != nil || !found {
		h.logger.Warn("claimed task unreadable",
			slog.String("task_id", id), slog.Bool("found", found))
		return
	}

	// Lease loss is a cancelation signal. If renewal ever reports the lease
	// gone, the sweeper has reclaimed this task and another worker may already
	// be running it — continuing here would execute the same task twice.
	taskCtx, cancelTask := context.WithCancel(ctx)
	defer cancelTask()
	renewStop := make(chan struct{})
	renewDone := make(chan struct{})
	go h.renewLease(taskCtx, id, cancelTask, renewStop, renewDone)
	defer func() { close(renewStop); <-renewDone }()

	if rec.TicketID != "" && h.claimer != nil {
		if err := h.claimer.ClaimTicket(ctx, rec.TicketID); err != nil {
			h.logger.Warn("failed to claim ticket (continuing execution)",
				slog.String("task_id", id),
				slog.String("ticket", rec.TicketID),
				slog.String("error", err.Error()))
		}
	}
	if ok, err := h.store.MarkRunning(ctx, id, h.owner); err != nil || !ok {
		h.logger.Warn("mark running refused; abandoning task",
			slog.String("task_id", id))
		return
	}

	maxAttempts := h.cfg.MaxRetries + 1
	var lastErr error
	for {
		attempt, err := h.store.BumpAttempt(taskCtx, id, h.owner)
		if err != nil {
			// ErrLeaseLost or a store failure: either way this worker has no
			// standing to continue or to record an outcome.
			h.logger.Warn("attempt bump failed; abandoning task",
				slog.String("task_id", id), slog.String("error", err.Error()))
			return
		}
		execCtx, cancel := context.WithTimeout(taskCtx, timeout)
		result, execErr := h.executor.ExecuteTask(execCtx, id, rec.Prompt)
		cancel()
		if execErr == nil {
			h.finishDurable(ctx, id, rec.TicketID, TaskStatusCompleted, result, "", attempt)
			return
		}
		lastErr = execErr
		h.logger.Warn("task execution failed",
			slog.String("task_id", id),
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", maxAttempts),
			slog.String("error", execErr.Error()))
		if attempt >= maxAttempts {
			break
		}
		backoff := time.Duration(1<<uint(attempt-1)) * time.Second //nolint:gosec // G115 shift bounded by maxAttempts
		select {
		case <-time.After(backoff):
		case <-taskCtx.Done():
			// Lease lost or shutdown mid-backoff. Leave the row claimed: the
			// sweeper requeues it at expiry with its attempt budget intact.
			return
		}
	}

	// Attribution comes from the final attempt's own error, never from the
	// ambient context: attempts exhausted on a live context is a plain
	// failure, and only a genuine per-attempt deadline reads as timed out.
	status := TaskStatusFailed
	if errors.Is(lastErr, context.DeadlineExceeded) {
		status = TaskStatusTimedOut
	}
	h.finishDurable(ctx, id, rec.TicketID, status, "",
		fmt.Sprintf("failed after %d attempts: %s", maxAttempts, lastErr.Error()), maxAttempts)
}

// renewLease extends the task lease at a third of the TTL until stopped. A
// renewal that returns false means the lease belongs to someone else now, so
// the task context is canceled to stop the executor. A renewal ERROR is
// treated as transient and retried — the lease itself decides liveness, not
// one flaky query.
func (h *Handler) renewLease(ctx context.Context, id string, cancelTask context.CancelFunc, stop, done chan struct{}) {
	defer close(done)
	interval := h.leaseTTL / 3
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			ok, err := h.store.Renew(ctx, id, h.owner, h.leaseTTL)
			if err != nil {
				h.logger.Warn("lease renewal error (retrying)",
					slog.String("task_id", id), slog.String("error", err.Error()))
				continue
			}
			if !ok {
				h.logger.Warn("lease lost; canceling task execution",
					slog.String("task_id", id))
				cancelTask()
				return
			}
		}
	}
}

// finishDurable records a terminal outcome, honoring the owner guard: a
// refused Finish means the lease was reclaimed mid-flight, and this worker's
// result — and its ticket completion — belong to nobody.
func (h *Handler) finishDurable(ctx context.Context, id, ticketID string, status TaskStatus, result, errMsg string, attempts int) {
	wrote, err := h.store.Finish(ctx, id, h.owner, status, result, errMsg)
	if err != nil {
		h.logger.Warn("finish write failed",
			slog.String("task_id", id), slog.String("error", err.Error()))
		return
	}
	if !wrote {
		h.logger.Warn("finish refused: lease lost; result discarded",
			slog.String("task_id", id))
		return
	}
	if rec, found, err := h.store.Get(ctx, id); err == nil && found {
		h.notify(&rec)
		h.logger.Info("task finished",
			slog.String("task_id", id),
			slog.String("status", string(status)),
			slog.Int("attempts", attempts))
	}
	if ticketID != "" && h.claimer != nil {
		evidence := result
		if status != TaskStatusCompleted {
			evidence = fmt.Sprintf("failed after %d attempts: %s", attempts, errMsg)
		}
		if len(evidence) > 500 {
			evidence = evidence[:500] + "..."
		}
		if err := h.claimer.CompleteTicket(ctx, ticketID, evidence); err != nil {
			h.logger.Warn("failed to complete ticket",
				slog.String("task_id", id),
				slog.String("ticket", ticketID),
				slog.String("error", err.Error()))
		}
	}
}

// StartLeaseSweeper begins the reclaim loop and returns a stop function that
// blocks until the loop has exited. Cadence is SweepInterval (default half
// the lease TTL), so a crashed worker's task is requeued within 2x TTL of its
// last renewal. A fresh process sweeping the same store requeues everything
// its predecessor held — restart recovery and crash recovery are the same
// mechanism, which is the durable-execution contract: stateless workers,
// externalized state.
func (h *Handler) StartLeaseSweeper(ctx context.Context) (stop func()) {
	if h.store == nil {
		return func() {}
	}
	// The sweep loop reads and writes the store on its own schedule, so it is
	// tracked like a task goroutine: Shutdown must join it too, not just the
	// tasks it dispatches. The returned stop still works on its own, for a
	// caller that wants to halt reclaiming without shutting the handler down.
	h.lifeMu.Lock()
	if h.stopping {
		h.lifeMu.Unlock()
		return func() {}
	}
	h.lifeWG.Add(1)
	h.lifeMu.Unlock()

	sctx, cancel := context.WithCancel(ctx)
	unwatch := context.AfterFunc(h.stopCtx, cancel)
	done := make(chan struct{})
	go func() {
		defer h.lifeWG.Done()
		defer close(done)
		defer unwatch()
		t := time.NewTicker(h.sweepInterval)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				h.sweepOnce(sctx)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// sweepOnce reclaims expired leases: dead-lettered tasks are announced to
// listeners; requeued tasks are redispatched locally through the normal
// bounded path (the semaphore still caps concurrent execution).
func (h *Handler) sweepOnce(ctx context.Context) {
	requeued, dead, err := h.store.ReclaimExpired(ctx, h.cfg.MaxRetries+1)
	if err != nil {
		h.logger.Warn("lease sweep failed", slog.String("error", err.Error()))
		return
	}
	for i := range dead {
		rec := &dead[i]
		h.logger.Warn("task dead-lettered after lease expiry",
			slog.String("task_id", rec.ID),
			slog.Int("attempts", rec.Attempts))
		h.notify(rec)
	}
	for i := range requeued {
		rec := &requeued[i]
		h.logger.Info("expired lease reclaimed; requeueing task",
			slog.String("task_id", rec.ID),
			slog.Int("attempts", rec.Attempts))
		timeout := h.cfg.DefaultTimeout
		if rec.TimeoutSecs > 0 {
			timeout = time.Duration(rec.TimeoutSecs) * time.Second
		}
		id := rec.ID
		if !h.spawn(ctx, func(taskCtx context.Context) {
			h.processTaskDurable(taskCtx, id, timeout)
		}) {
			// Shutting down: leave the row pending. It is already requeued in
			// the store, so the next sweeper to run claims it.
			return
		}
	}
}
