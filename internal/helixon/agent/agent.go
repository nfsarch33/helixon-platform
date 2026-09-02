// Package agent implements the Helixon agent runtime: lifecycle, state, and tool dispatch.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nfsarch33/helixon-platform/internal/callbacks"
	"github.com/nfsarch33/helixon-platform/internal/llm"
)

var (
	ErrMaxIterations = errors.New("agent: max iterations exceeded")
	ErrBudgetExhaust = errors.New("agent: token budget exhausted")
	ErrTimeout       = errors.New("agent: execution timeout")
)

// ToolExecutor dispatches tool calls and returns results.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, argsJSON string) (string, error)
	Available() []llm.Tool
}

// RunObserver receives per-iteration telemetry from the loop.
//
// It is an interface declared HERE, and satisfied elsewhere, so the agent loop
// carries no dependency on any instrumentation library: the loop is the code
// that must stay readable, and a metrics import in it would be the first of
// many. A nil Observer is the normal case for library callers.
type RunObserver interface {
	// ObserveLoopIteration is called once per iteration the loop commits to.
	ObserveLoopIteration()
	// ObserveTokens is called with the REAL usage the provider reported for
	// one iteration. Before v18779 these were hard-coded to zero everywhere,
	// so the cost of a run was unrecoverable after the fact.
	ObserveTokens(promptTokens, completionTokens int)
}

// Config controls agent loop behavior.
type Config struct {
	MaxIterations int
	MaxTokens     int
	Timeout       time.Duration
	SystemPrompt  string
	Logger        *slog.Logger
	// Completion gates "the model stopped calling tools" behind verifier
	// evidence. The zero value is filled from DefaultCompletionPolicy
	// except for Enabled, which stays as supplied so a library caller that
	// never heard of the gate keeps its existing behavior; the CLI turns
	// it on explicitly.
	Completion CompletionPolicy
	// Observer, when non-nil, receives loop telemetry. Optional.
	Observer RunObserver
	// LeaseTTL is how long a worker's claim on a run stays valid without a
	// renewal; a run whose lease lapses is resumable by any worker. Default 30s.
	LeaseTTL time.Duration
	// MaxRunAttempts caps how many times an interrupted run is claimed before
	// the recovery sweep dead-letters it. Default 3.
	MaxRunAttempts int
}

func (c Config) withDefaults() Config {
	if c.MaxIterations <= 0 {
		c.MaxIterations = 25
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 128000
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Minute
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = defaultLeaseTTL
	}
	if c.MaxRunAttempts <= 0 {
		c.MaxRunAttempts = 3
	}
	c.Completion = c.Completion.withDefaults()
	return c
}

// Agent runs a tool-augmented conversation loop against an LLM provider.
type Agent struct {
	provider llm.Provider
	tools    ToolExecutor
	store    *SessionStore
	cfg      Config
	logger   *slog.Logger
	// owner identifies this instance as a run-lease owner (see run_durable.go).
	owner string
	// active is the set of run ids this instance is executing right now. The
	// lease alone cannot refuse a second entry by the SAME owner, and two loops
	// on one run in one process would be the duplicated side effect the lease
	// exists to prevent.
	activeMu sync.Mutex
	active   map[string]struct{}
}

// New creates an Agent wired to the given provider, tool executor, and session store.
func New(provider llm.Provider, tools ToolExecutor, store *SessionStore, cfg Config) *Agent {
	cfg = cfg.withDefaults()
	return &Agent{
		provider: provider,
		tools:    tools,
		store:    store,
		cfg:      cfg,
		logger:   cfg.Logger.With(slog.String("component", "helixon.agent")),
		owner:    newOwner(),
		active:   map[string]struct{}{},
	}
}

// RunResult captures the outcome of a full agent run.
type RunResult struct {
	SessionID    string `json:"session_id"`
	FinalContent string `json:"final_content"`
	Iterations   int    `json:"iterations"`
	TokensIn     int    `json:"tokens_in"`
	TokensOut    int    `json:"tokens_out"`
	// Mutated records that the run used a state-changing tool, which is
	// what makes it subject to the verifier evidence requirement.
	Mutated bool `json:"mutated"`
	// VerifierPassed records that at least one verifier check passed and
	// that no verifier check has failed since.
	VerifierPassed bool `json:"verifier_passed"`
	// VerifierFailures counts CONSECUTIVE verifier failures; it resets on a
	// pass, because the escalation policy is about a stuck loop, not about
	// a run that had one red check and then fixed it.
	VerifierFailures int `json:"verifier_failures"`
	// NeedsHumanApproval marks a run that stopped for a human rather than
	// finishing or retrying.
	NeedsHumanApproval bool  `json:"needs_human_approval"`
	Err                error `json:"-"`
}

// Run executes the agent loop: send user message, handle tool calls in a loop
// until the model produces a final text response or limits are reached.
//
// The orchestrator is split into focused helpers so each has low CC and is
// independently testable (refactor v17804-5 from CC 15):
//
//	startRun                - setup timeout, append user turn, allocate result
//	iterateRun              - one loop iteration: budget-check, model call,
//	                         tool-execute, finalize decision
//	checkRunTermination     - budget + timeout guard returning typed errors
//	invokeModel             - build completion request, call provider.Complete
//	recordAssistantTurn     - persist assistant turn with optional tool payload
//	finalizeRun             - decide final/continue based on tool-call presence
func (a *Agent) Run(ctx context.Context, sessionID, userMessage string) (*RunResult, error) {
	// Every entry point is durable: a fresh run id, the user turn written in
	// the same transaction as the run row, a lease, and a terminal record.
	// See run_durable.go for the lifecycle and for Resume.
	return a.RunDurable(ctx, uuid.New().String(), sessionID, userMessage, nil)
}

// iterateRun runs one model iteration and reports whether the loop can exit.
// Returning final=true means the caller should stop iterating; final=false
// means tool calls were executed and the next iteration is required.
//
// Returned error covers: budget/timeout guard, build failure, model failure,
// store failure, or tool-execute failure.
func (a *Agent) iterateRun(ctx context.Context, runID, sessionID string, iter int, userMessage string, result *RunResult) (final bool, err error) {
	if err := checkRunTermination(ctx, result, iter, a.cfg.MaxTokens, a.cfg.MaxIterations); err != nil {
		return false, err
	}
	// Counted here, not after the model answers: an iteration the loop
	// committed to but that died at the provider is still an iteration, and
	// counting only the successful ones would make a run that burned its whole
	// budget on transport errors look like a run that never started.
	if a.cfg.Observer != nil {
		a.cfg.Observer.ObserveLoopIteration()
	}
	messages, err := a.buildMessages(ctx, sessionID)
	if err != nil {
		return false, storeErr(ctx, "build messages", err)
	}
	ctx = a.notifyRunStart(ctx, runID, iter, userMessage)

	resp, err := a.invokeModel(ctx, sessionID, messages, iter)
	if err != nil {
		return false, err
	}
	choice := resp.Choices[0]
	result.TokensIn += resp.Usage.PromptTokens
	result.TokensOut += resp.Usage.CompletionTokens
	if a.cfg.Observer != nil {
		a.cfg.Observer.ObserveTokens(resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}
	a.logger.Debug("agent iteration",
		slog.Int("iteration", iter),
		slog.Int("tool_calls", len(choice.Message.ToolCalls)),
		slog.Int("tokens_in", resp.Usage.PromptTokens),
		slog.Int("tokens_out", resp.Usage.CompletionTokens),
	)
	assistantSeq, err := a.recordAssistantTurn(ctx, runID, sessionID, &choice, resp.Usage)
	if err != nil {
		return false, err
	}
	done, err := finalizeRun(result, choice.Message.Content, len(choice.Message.ToolCalls))
	if err != nil {
		return done, err
	}
	if done {
		return true, a.gateCompletion(result)
	}
	// The budget verdict is reached HERE: on the numbers the provider just
	// reported, and before this iteration's tool calls are dispatched.
	//
	// The position is load-bearing in three ways. It is AFTER
	// recordAssistantTurn because the provider bills for the response that
	// blew the budget, so it has to be written down — v18779 added the
	// per-turn token columns precisely so a run's cost stayed recoverable, and
	// dropping the last turn would undercount every over-budget run by its
	// single most expensive call. It is AFTER the `done` branch because a run
	// that has already produced its final answer has finished, and failing it
	// for a limit it crossed on the way to succeeding would be perverse. And
	// it is BEFORE executeToolCalls because the tool calls, not the write, are
	// what the budget is protecting against: checking only at the top of the
	// next iteration let a run that had already blown its budget dispatch a
	// whole further round of tool calls, with whatever side effects those
	// carry. A limit enforced one full iteration late is not enforcing much.
	if err := checkTokenBudget(result, a.cfg.MaxTokens); err != nil {
		return false, err
	}
	if err := a.executeToolCalls(ctx, runID, sessionID, iter, assistantSeq, choice.Message.ToolCalls, result); err != nil {
		return false, err
	}
	return false, nil
}

// executeToolCalls runs each tool call sequentially and persists a tool turn
// per call. It also maintains the completion-gate bookkeeping: which tools
// changed state, and whether the verifier has passed or failed consecutively.
//
// Every call is bracketed by a durable step: BeginStep before dispatch,
// FinishStep after the tool turn is written. On a resumed run BeginStep
// finds the step already there, and settleReplayedStep decides from the
// durable log whether the call is done, must be re-run, or must stop the run
// for a human (a mutating call with no recorded outcome).
//
// Returns a wrapped error if the store rejects any write, ErrInterruptedMutation
// for the unknown-outcome case, or ErrNeedsHumanApproval once the verifier has
// failed MaxConsecutiveFailures times in a row — the loop stops there rather
// than retrying forever.
//
// iteration scopes the step key (tool call ids repeat across responses for
// some providers) and assistantSeq bounds the tool-turn lookup on replay.
func (a *Agent) executeToolCalls(ctx context.Context, runID, sessionID string, iteration int, assistantSeq int64, calls []llm.ToolCall, result *RunResult) error {
	for _, tc := range calls {
		name := tc.Function.Name
		step, created, err := a.store.BeginStep(ctx, runID, iteration, tc.ID, name, tc.Function.Arguments)
		if err != nil {
			return storeErr(ctx, "begin step", err)
		}
		if !created {
			settled, err := a.settleReplayedStep(ctx, runID, sessionID, assistantSeq, step, result)
			if err != nil {
				return err
			}
			if settled {
				continue
			}
		}
		toolResult, toolErr := a.tools.Execute(ctx, name, tc.Function.Arguments)
		if a.cfg.Completion.isMutating(name) {
			result.Mutated = true
		}
		escalate := a.recordVerifierOutcome(name, toolResult, toolErr, result)
		if toolErr != nil {
			toolResult = fmt.Sprintf("error: %s", toolErr.Error())
		}
		if _, err := a.store.AppendRunTurn(ctx, runID, a.owner, sessionID, RoleTool, toolResult, nil, tc.ID, 0, 0); err != nil {
			return storeErr(ctx, "append tool turn", err)
		}
		stepStatus := StepDone
		if toolErr != nil {
			stepStatus = StepFailed
		}
		if err := a.store.FinishStep(ctx, runID, iteration, tc.ID, stepStatus, toolResult); err != nil {
			return storeErr(ctx, "finish step", err)
		}
		if escalate {
			result.NeedsHumanApproval = true
			result.Err = ErrNeedsHumanApproval
			a.logger.Warn("verifier failed repeatedly; escalating for human approval",
				slog.String("session_id", sessionID),
				slog.String("tool", name),
				slog.Int("consecutive_failures", result.VerifierFailures),
			)
			return ErrNeedsHumanApproval
		}
	}
	return nil
}

// settleReplayedStep decides what a resumed run does with a tool call it has
// seen before. It returns settled=true when nothing must be dispatched:
//
//   - the step already finished: its tool turn is in the log;
//   - the step is pending but its tool turn exists: the crash landed between
//     the append and FinishStep, so the turn IS the outcome - close the step;
//   - the step is pending, no tool turn, and the tool is mutating: the outcome
//     is unknowable, so the run stops for a human (ErrInterruptedMutation)
//     with a tool turn saying so, rather than risk a second side effect.
//
// It returns settled=false for a pending, unrecorded, non-mutating call:
// re-reading is free of side effects, so the caller dispatches it again.
func (a *Agent) settleReplayedStep(ctx context.Context, runID, sessionID string, assistantSeq int64, step *RunStep, result *RunResult) (bool, error) {
	if step.Status != StepPending {
		if a.cfg.Completion.isMutating(step.Tool) {
			result.Mutated = true
		}
		return true, nil
	}
	recorded, err := a.store.ToolTurnExists(ctx, sessionID, assistantSeq, step.ToolCallID)
	if err != nil {
		return false, storeErr(ctx, "look up tool turn", err)
	}
	if recorded {
		if err := a.store.FinishStep(ctx, runID, step.Iteration, step.ToolCallID, StepDone, ""); err != nil {
			return false, storeErr(ctx, "close replayed step", err)
		}
		if a.cfg.Completion.isMutating(step.Tool) {
			result.Mutated = true
		}
		return true, nil
	}
	if !a.cfg.Completion.isMutating(step.Tool) {
		return false, nil
	}
	note := fmt.Sprintf("error: tool %s was interrupted before its outcome was recorded; a human must confirm its effect before the run continues", step.Tool)
	if _, err := a.store.AppendRunTurn(ctx, runID, a.owner, sessionID, RoleTool, note, nil, step.ToolCallID, 0, 0); err != nil {
		return false, storeErr(ctx, "append interrupted tool turn", err)
	}
	if err := a.store.FinishStep(ctx, runID, step.Iteration, step.ToolCallID, StepFailed, note); err != nil {
		return false, storeErr(ctx, "fail interrupted step", err)
	}
	result.Mutated = true
	result.NeedsHumanApproval = true
	result.Err = ErrInterruptedMutation
	a.logger.Warn("resumed run found a mutating tool call with no recorded outcome; stopping for human approval",
		slog.String("run_id", runID), slog.String("tool", step.Tool), slog.String("tool_call_id", step.ToolCallID))
	return true, ErrInterruptedMutation
}

// recordVerifierOutcome updates the verifier bookkeeping for one tool call
// and reports whether the escalation threshold has just been crossed.
func (a *Agent) recordVerifierOutcome(name, payload string, toolErr error, result *RunResult) bool {
	if !a.gateActive() || name != a.cfg.Completion.VerifierTool {
		return false
	}
	if parseVerifierVerdict(payload, toolErr) {
		result.VerifierPassed = true
		result.VerifierFailures = 0
		return false
	}
	result.VerifierPassed = false
	result.VerifierFailures++
	return result.VerifierFailures >= a.cfg.Completion.MaxConsecutiveFailures
}

// checkRunTermination returns ErrBudgetExhaust when the in+out token sum is
// greater than MaxTokens, and ErrTimeout when ctx is done. iter and maxIter
// are reserved for the future iterations-overflow guard.
//
// The budget is checked FIRST, and the order is load-bearing. Under load a run
// crosses both limits before the same check: the tokens were spent before the
// clock ran out, so budget exhaustion is the true stop reason — and the two
// verdicts travel different roads from here. A timeout is classified
// retryable and re-runs the work; a blown budget is a policy stop, and
// re-running it re-pays every call the budget already refused. The reason also
// feeds the escalations metric, and separating budget_exhausted from timeout
// there is the point of having the label. The budget verdict is decidable from
// numbers the loop already holds; ctx.Err() merely reports what the clock did
// in the meantime, so it goes second.
func checkRunTermination(ctx context.Context, r *RunResult, iter, maxTokens, maxIter int) error { //nolint:revive // unused-parameter required by interface
	if err := checkTokenBudget(r, maxTokens); err != nil {
		return err
	}
	if ctx.Err() != nil {
		r.Err = ErrTimeout
		return ErrTimeout
	}
	return nil
}

// checkTokenBudget returns ErrBudgetExhaust when the in+out token sum is
// greater than maxTokens. It is deliberately free of I/O and of the context:
// the budget is a property of numbers the loop already holds, so the verdict
// is decidable without waiting on anything and cannot be pre-empted by a slow
// durable write.
func checkTokenBudget(r *RunResult, maxTokens int) error {
	if r.TokensIn+r.TokensOut > maxTokens {
		r.Err = ErrBudgetExhaust
		return ErrBudgetExhaust
	}
	return nil
}

// storeErr classifies a session-store failure.
//
// A store call that failed because the RUN's own deadline expired is a run
// timeout, not a storage fault. Reporting it verbatim names the wrong
// subsystem — "append tool turn: insert turn: context deadline exceeded" says
// nothing about which limit the run actually hit — so the typed ErrTimeout is
// added to the chain while the underlying cause is preserved for diagnostics.
//
// The test is specifically DeadlineExceeded, not ctx.Err() != nil, because
// that broader test misattributes two other things as an execution timeout:
// an upstream cancellation (the caller went away — telling an operator to
// raise Config.Timeout sends them nowhere), and any failure on a path that has
// already cancelled the context itself, which would relabel a foreign-key
// rejection or a full disk as a timeout zero seconds into the budget.
//
// Two %w verbs rather than errors.Join: Join separates its operands with a
// NEWLINE, which splits one failure across two records in a line-oriented log.
// Both errors stay reachable through errors.Is either way.
func storeErr(ctx context.Context, op string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w: %w", op, ErrTimeout, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// notifyRunStart fires the callbacks.OnStart hook if a handler is registered.
// Returns the (possibly wrapped) context for downstream callers.
func (a *Agent) notifyRunStart(ctx context.Context, sessionID string, iter int, userMessage string) context.Context {
	handler := callbacks.HandlerFromContext(ctx)
	if handler == nil {
		return ctx
	}
	info := &callbacks.RunInfo{
		ComponentName: "helixon.agent",
		RunID:         sessionID,
		StartedAt:     time.Now(),
		Tags:          map[string]string{"iteration": fmt.Sprintf("%d", iter)},
	}
	return handler.OnStart(ctx, info, userMessage)
}

// invokeModel builds the CompletionRequest and calls provider.Complete.
// Wraps transport errors with the iteration number for diagnostics.
func (a *Agent) invokeModel(ctx context.Context, sessionID string, messages []llm.Message, iter int) (*llm.CompletionResponse, error) { //nolint:revive // unused-parameter required by interface
	req := llm.CompletionRequest{Messages: messages, Tools: a.tools.Available()}
	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm complete (iter %d): %w", iter, err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty response at iteration %d", iter)
	}
	return resp, nil
}

// recordAssistantTurn persists the assistant turn with optional tool-call
// payload and the REAL token usage for the iteration that produced it.
//
// v18779: these two columns were hard-coded to 0 at every call site, so
// SessionTokenUsage summed to zero for every session ever recorded and the
// per-turn cost of a run was unrecoverable after the fact. The provider
// already returns Usage on each response; it just was not being written down.
//
// It returns the turn's seq, which bounds the replay lookup for this
// iteration's tool calls.
func (a *Agent) recordAssistantTurn(ctx context.Context, runID, sessionID string, choice *llm.Choice, usage llm.Usage) (int64, error) {
	var toolCallsJSON json.RawMessage
	if len(choice.Message.ToolCalls) > 0 {
		toolCallsJSON, _ = json.Marshal(choice.Message.ToolCalls)
	}
	turn, err := a.store.AppendRunTurn(ctx, runID, a.owner, sessionID, RoleAssistant, choice.Message.Content,
		toolCallsJSON, "", usage.PromptTokens, usage.CompletionTokens)
	if err != nil {
		return 0, storeErr(ctx, "append assistant turn", err)
	}
	return turn.Seq, nil
}

// finalizeRun inspects the assistant content and tool-call presence on the
// caller-supplied result. Returns (final=true, nil) when the model emitted
// no tool calls (caller stores FinalContent and returns); (final=false, nil)
// when tool calls are pending. The toolCallCount argument lets the helper
// stay testable without constructing a full llm.Choice.
//
// Semantics match the production branch in Run:
//
//	len(choice.Message.ToolCalls) == 0 -> final=true,  set FinalContent
//	otherwise                            -> final=false, continue loop
func finalizeRun(r *RunResult, content string, toolCallCount int) (bool, error) { //nolint:unparam // error return reserved for tool-call dispatch failures
	if toolCallCount == 0 {
		r.FinalContent = content
		return true, nil
	}
	return false, nil
}

// buildMessages reconstructs the full message history from the session store.
func (a *Agent) buildMessages(ctx context.Context, sessionID string) ([]llm.Message, error) {
	turns, err := a.store.ListTurns(ctx, sessionID, 0)
	if err != nil {
		return nil, err
	}

	msgs := make([]llm.Message, 0, len(turns)+1)

	if a.cfg.SystemPrompt != "" {
		msgs = append(msgs, llm.Message{
			Role:    string(RoleSystem),
			Content: a.cfg.SystemPrompt,
		})
	}

	for _, t := range turns {
		msg := llm.Message{
			Role:       string(t.Role),
			Content:    t.Content,
			ToolCallID: t.ToolCallID,
		}
		if len(t.ToolCalls) > 0 {
			var tcs []llm.ToolCall
			if err := json.Unmarshal(t.ToolCalls, &tcs); err == nil {
				msg.ToolCalls = tcs
			}
		}
		msgs = append(msgs, msg)
	}

	return msgs, nil
}
