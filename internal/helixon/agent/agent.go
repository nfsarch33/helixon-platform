// Package agent implements the Helixon agent runtime: lifecycle, state, and tool dispatch.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

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
	ctx, result, cleanup, err := a.startRun(ctx, sessionID, userMessage)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	for iter := 0; iter < a.cfg.MaxIterations; iter++ {
		result.Iterations = iter + 1
		final, err := a.iterateRun(ctx, sessionID, iter+1, userMessage, result)
		if err != nil {
			return result, err
		}
		if final {
			return result, nil
		}
	}
	result.Err = ErrMaxIterations
	return result, ErrMaxIterations
}

// startRun applies the agent timeout, persists the user turn, and returns the
// derived context, a fresh RunResult, and a cleanup func that releases the
// timeout-derived resources when the caller is done.
func (a *Agent) startRun(ctx context.Context, sessionID, userMessage string) (context.Context, *RunResult, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	cleanup := func() { cancel() }
	if _, err := a.store.AppendTurn(ctx, sessionID, RoleUser, userMessage, nil, "", 0, 0); err != nil {
		// Classified BEFORE cleanup: cleanup cancels ctx, and a cancelled
		// context must not be allowed to decide why the store failed.
		wrapped := storeErr(ctx, "append user turn", err)
		cleanup()
		return nil, nil, func() {}, wrapped
	}
	return ctx, &RunResult{SessionID: sessionID}, cleanup, nil
}

// iterateRun runs one model iteration and reports whether the loop can exit.
// Returning final=true means the caller should stop iterating; final=false
// means tool calls were executed and the next iteration is required.
//
// Returned error covers: budget/timeout guard, build failure, model failure,
// store failure, or tool-execute failure.
func (a *Agent) iterateRun(ctx context.Context, sessionID string, iter int, userMessage string, result *RunResult) (final bool, err error) {
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
	ctx = a.notifyRunStart(ctx, sessionID, iter, userMessage)

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
	if err := a.recordAssistantTurn(ctx, sessionID, &choice, resp.Usage); err != nil {
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
	if err := a.executeToolCalls(ctx, sessionID, choice.Message.ToolCalls, result); err != nil {
		return false, err
	}
	return false, nil
}

// executeToolCalls runs each tool call sequentially and persists a tool turn
// per call. It also maintains the completion-gate bookkeeping: which tools
// changed state, and whether the verifier has passed or failed consecutively.
//
// Returns a wrapped error if the store rejects any append, or
// ErrNeedsHumanApproval once the verifier has failed MaxConsecutiveFailures
// times in a row — the loop stops there rather than retrying forever.
func (a *Agent) executeToolCalls(ctx context.Context, sessionID string, calls []llm.ToolCall, result *RunResult) error {
	for _, tc := range calls {
		name := tc.Function.Name
		toolResult, toolErr := a.tools.Execute(ctx, name, tc.Function.Arguments)
		if a.cfg.Completion.isMutating(name) {
			result.Mutated = true
		}
		escalate := a.recordVerifierOutcome(name, toolResult, toolErr, result)
		if toolErr != nil {
			toolResult = fmt.Sprintf("error: %s", toolErr.Error())
		}
		if _, err := a.store.AppendTurn(ctx, sessionID, RoleTool, toolResult, nil, tc.ID, 0, 0); err != nil {
			return storeErr(ctx, "append tool turn", err)
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

// checkRunTermination returns ErrTimeout when ctx is done and ErrBudgetExhaust
// when the in+out token sum is greater than MaxTokens. iter and maxIter are
// reserved for the future iterations-overflow guard.
func checkRunTermination(ctx context.Context, r *RunResult, iter, maxTokens, maxIter int) error { //nolint:revive // unused-parameter required by interface
	if ctx.Err() != nil {
		r.Err = ErrTimeout
		return ErrTimeout
	}
	return checkTokenBudget(r, maxTokens)
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
func (a *Agent) recordAssistantTurn(ctx context.Context, sessionID string, choice *llm.Choice, usage llm.Usage) error {
	var toolCallsJSON json.RawMessage
	if len(choice.Message.ToolCalls) > 0 {
		toolCallsJSON, _ = json.Marshal(choice.Message.ToolCalls)
	}
	if _, err := a.store.AppendTurn(ctx, sessionID, RoleAssistant, choice.Message.Content,
		toolCallsJSON, "", usage.PromptTokens, usage.CompletionTokens); err != nil {
		return storeErr(ctx, "append assistant turn", err)
	}
	return nil
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
