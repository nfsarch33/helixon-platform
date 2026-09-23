// Package leaselost classifies a claim-renew outcome for the fleet poller.
//
// A renew that returns 409 means the lease has been swept or taken by another
// run; continuing the run would either waste work or double-work a ticket, so
// it is the only outcome that should cancel the run. 404 indicates a board
// that predates the route and is treated as best-effort. 429 and 5xx, and any
// transport error, are transient and should be retried.
package leaselost

import "strconv"

// Outcome describes how the poller should react to a single renew response.
type Outcome int

const (
	// Renewed is a 2xx response: the claim is held.
	Renewed Outcome = iota
	// BestEffort is a non-renewing status (404/400/401/403 or other): keep going.
	BestEffort
	// LeaseLost is a 409: the lease is gone, cancel the run.
	LeaseLost
	// Retry is transient: 429, any 5xx, or a transport error.
	Retry
)

func (o Outcome) String() string {
	switch o {
	case Renewed:
		return "renewed"
	case BestEffort:
		return "best_effort"
	case LeaseLost:
		return "lease_lost"
	case Retry:
		return "retry"
	}
	return strconv.Itoa(int(o))
}

// Classify maps a renew (status, err) pair to an Outcome.
func Classify(status int, err error) Outcome {
	if err != nil {
		return Retry
	}
	switch {
	case status >= 200 && status <= 299:
		return Renewed
	case status == 404 || status == 400 || status == 401 || status == 403:
		return BestEffort
	case status == 409:
		return LeaseLost
	case status == 429 || (status >= 500 && status <= 599):
		return Retry
	}
	return BestEffort
}

// ShouldCancelRun reports whether the run should be canceled for this outcome.
// Only LeaseLost returns true.
func ShouldCancelRun(o Outcome) bool {
	return o == LeaseLost
}

// Tracker counts consecutive Retry outcomes so the poller can give up after a
// threshold instead of looping forever on a sick board.
type Tracker struct {
	consecutiveRetries int
}

// Record updates the streak for outcome o and returns the new consecutive
// Retry count. Any non-Retry outcome resets the streak to 0.
func (t *Tracker) Record(o Outcome) int {
	if o != Retry {
		t.consecutiveRetries = 0
		return 0
	}
	t.consecutiveRetries++
	return t.consecutiveRetries
}

// Exhausted reports whether the streak has reached maxRetries. A non-positive
// maxRetries disables exhaustion and always returns false.
func (t *Tracker) Exhausted(maxRetries int) bool {
	return maxRetries > 0 && t.consecutiveRetries >= maxRetries
}
