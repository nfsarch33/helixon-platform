package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestStoreFailureUnrelatedToTheDeadlineIsNotATimeout.
//
// storeErr exists to stop a run timeout being reported as a storage fault. It
// must not do the reverse. startRun cancels the run context on the error path,
// so anything that reads ctx.Err() AFTER that cancel sees a cancelled context
// no matter why the store actually failed — and labels a foreign-key
// rejection, a full disk, or a bad session ID "agent: execution timeout".
func TestStoreFailureUnrelatedToTheDeadlineIsNotATimeout(t *testing.T) {
	store := newTestStore(t)
	ag := New(&mockProvider{}, &mockToolExecutor{}, store, Config{
		MaxIterations: 3,
		MaxTokens:     1000,
		// Generous on purpose: nothing here may be attributed to a deadline.
		Timeout: 30 * time.Second,
	})

	_, err := ag.Run(context.Background(), "no-such-session", "hi")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTimeout),
		"a foreign-key rejection is not a run timeout; got %v", err)
	assert.Contains(t, err.Error(), "FOREIGN KEY",
		"the real cause must survive in the message")
}

// TestRunDeadlineStoreFailureIsATimeout is the positive control for the test
// above: when the run's own deadline really has expired, a store failure must
// still carry ErrTimeout. Without this, "never report ErrTimeout" would pass.
func TestRunDeadlineStoreFailureIsATimeout(t *testing.T) {
	store := newTestStore(t)
	sess, err := store.CreateSession(context.Background(), "deadline", nil)
	require.NoError(t, err)

	ag := New(&mockProvider{}, &mockToolExecutor{}, store, Config{
		MaxIterations: 3,
		MaxTokens:     1000,
		// Already expired by the time the first write is attempted.
		Timeout: time.Nanosecond,
	})

	_, err = ag.Run(context.Background(), sess.ID, "hi")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTimeout),
		"a store failure caused by the run's own deadline is a timeout; got %v", err)
}

// TestStoreErrIsSingleLine — errors.Join separates its operands with a
// newline, which splits one failure across two records in any line-oriented
// log. The whole point of this wrapper is a message an operator can read.
func TestStoreErrIsSingleLine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	err := storeErr(ctx, "append tool turn", errors.New("insert turn: disk full"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\n", "error message must stay on one line")
	// Both the verdict and the cause must remain reachable.
	assert.True(t, errors.Is(err, ErrTimeout))
	assert.Contains(t, err.Error(), "disk full")
}

// TestCallerCancellationIsNotAnExecutionTimeout — ctx.Err() is also non-nil
// for context.Canceled. An operator told "agent: execution timeout" for a
// client disconnect will go and raise Config.Timeout, and nothing will change.
func TestCallerCancellationIsNotAnExecutionTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := storeErr(ctx, "append tool turn", errors.New("insert turn: interrupted"))
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTimeout),
		"an upstream cancellation is not the agent's own deadline; got %v", err)
	assert.Contains(t, err.Error(), "interrupted")
}

// TestBudgetExhaustStillAccountsForTheFinalCall.
//
// The provider bills for the response that blows the budget, so it has to be
// written down. v18779 added per-turn token columns precisely so a run's cost
// was recoverable afterwards; dropping the last assistant turn undercounts
// every over-budget run by its single most expensive call.
func TestBudgetExhaustStillAccountsForTheFinalCall(t *testing.T) {
	resp := &llm.CompletionResponse{
		Choices: []llm.Choice{{
			Message: llm.Message{
				Role:      "assistant",
				ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "noop", Arguments: "{}"}}},
			},
		}},
		Usage: llm.Usage{PromptTokens: 5000, CompletionTokens: 5000},
	}
	responses := make([]*llm.CompletionResponse, 20)
	for i := range responses {
		responses[i] = resp
	}
	store := newTestStore(t)
	ag := New(&mockProvider{responses: responses}, &mockToolExecutor{
		tools:   []llm.Tool{{Type: "function", Function: llm.FunctionDef{Name: "noop"}}},
		results: map[string]string{"noop": "ok"},
	}, store, Config{MaxIterations: 20, MaxTokens: 15000, Timeout: 10 * time.Second})

	sess, err := store.CreateSession(context.Background(), "accounting", nil)
	require.NoError(t, err)

	result, err := ag.Run(context.Background(), sess.ID, "expensive query")
	require.ErrorIs(t, err, ErrBudgetExhaust)

	inDB, outDB, err := store.SessionTokenUsage(context.Background(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, result.TokensIn, inDB,
		"every prompt token the run reports must be recoverable from the store")
	assert.Equal(t, result.TokensOut, outDB,
		"every completion token the run reports must be recoverable from the store")
}

// TestBudgetExhaustStopsBeforeTheNextToolCall — the side effects, not the
// write, are what the budget guard is protecting against. Pairs with the test
// above: together they pin "record the turn, then refuse to act on it".
func TestBudgetExhaustStopsBeforeTheNextToolCall(t *testing.T) {
	resp := &llm.CompletionResponse{
		Choices: []llm.Choice{{
			Message: llm.Message{
				Role:      "assistant",
				ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "noop", Arguments: "{}"}}},
			},
		}},
		Usage: llm.Usage{PromptTokens: 5000, CompletionTokens: 5000},
	}
	responses := make([]*llm.CompletionResponse, 20)
	for i := range responses {
		responses[i] = resp
	}
	tools := &countingToolExecutor{
		mockToolExecutor: mockToolExecutor{
			tools:   []llm.Tool{{Type: "function", Function: llm.FunctionDef{Name: "noop"}}},
			results: map[string]string{"noop": "ok"},
		},
	}
	store := newTestStore(t)
	ag := New(&mockProvider{responses: responses}, tools, store,
		Config{MaxIterations: 20, MaxTokens: 15000, Timeout: 10 * time.Second})

	sess, err := store.CreateSession(context.Background(), "sideeffects", nil)
	require.NoError(t, err)

	_, err = ag.Run(context.Background(), sess.ID, "expensive query")
	require.ErrorIs(t, err, ErrBudgetExhaust)

	// Iteration 1 is inside budget and runs its tool. Iteration 2's response
	// crosses the limit, so its tool call must NOT be dispatched.
	assert.Equal(t, 1, tools.calls,
		"no tool may be executed on the iteration that exhausted the budget")
}

type countingToolExecutor struct {
	mockToolExecutor
	calls int
}

func (c *countingToolExecutor) Execute(ctx context.Context, name, args string) (string, error) {
	c.calls++
	return c.mockToolExecutor.Execute(ctx, name, args)
}

// TestWithStorePragmasMergesWithCallerPragmas — a caller who sets one pragma
// must not silently lose the rest. Losing foreign_keys(ON) turns the
// ON DELETE CASCADE back off; losing synchronous(NORMAL) puts every turn write
// back behind an fsync barrier. Both fail silently, which is how they got here.
func TestWithStorePragmasMergesWithCallerPragmas(t *testing.T) {
	got := withStorePragmas("/var/lib/helixon/agent.db?_pragma=busy_timeout(10000)")

	assert.Contains(t, got, "_pragma=busy_timeout(10000)", "caller's value must win")
	assert.NotContains(t, got, "_pragma=busy_timeout(5000)", "ours must not be added twice")
	for _, required := range []string{
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(ON)",
		"_pragma=journal_mode(WAL)",
	} {
		assert.Contains(t, got, required, "must survive a caller-supplied pragma")
	}
	assert.Equal(t, 1, strings.Count(got, "?"), "must stay a single query string")
}

// And the behavioural half: a store opened from such a DSN still enforces the
// cascade and still writes at synchronous=NORMAL.
func TestCallerPragmaDsnStillGetsTheStoreDefaults(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "merged.db") + "?_pragma=busy_timeout(10000)"
	store, err := NewSessionStore(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	var sync, fk, busy int
	require.NoError(t, store.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync))
	require.NoError(t, store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk))
	require.NoError(t, store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy))
	assert.Equal(t, 1, sync, "synchronous must still be NORMAL")
	assert.Equal(t, 1, fk, "foreign_keys must still be ON")
	assert.Equal(t, 10000, busy, "the caller's busy_timeout must win")
}
