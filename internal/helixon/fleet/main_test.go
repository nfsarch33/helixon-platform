package fleet

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces the harness-engineering default for a package that spawns
// goroutines: prove they do not leak. VerifyTestMain checks at process end
// (with its own settling retries), so short-lived task goroutines must have
// finished by then — a stuck renewal loop, sweeper, or semaphore waiter fails
// the whole package rather than surviving unseen.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
