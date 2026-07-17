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

// Config controls agent loop behavior.
type Config struct {
	MaxIterations int
	MaxTokens     int
	Timeout       time.Duration
	SystemPrompt  string
	Logger        *slog.Logger
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
	Err          error  `json:"-"`
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
		cleanup()
		return nil, nil, func() {}, fmt.Errorf("append user turn: %w", err)
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
	messages, err := a.buildMessages(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("build messages: %w", err)
	}
	ctx = a.notifyRunStart(ctx, sessionID, iter, userMessage)

	resp, err := a.invokeModel(ctx, sessionID, messages, iter)
	if err != nil {
		return false, err
	}
	choice := resp.Choices[0]
	result.TokensIn += resp.Usage.PromptTokens
	result.TokensOut += resp.Usage.CompletionTokens
	a.logger.Debug("agent iteration",
		slog.Int("iteration", iter),
		slog.Int("tool_calls", len(choice.Message.ToolCalls)),
		slog.Int("tokens_in", resp.Usage.PromptTokens),
		slog.Int("tokens_out", resp.Usage.CompletionTokens),
	)
	if err := a.recordAssistantTurn(ctx, sessionID, &choice); err != nil {
		return false, err
	}
	done, err := finalizeRun(result, choice.Message.Content, len(choice.Message.ToolCalls))
	if err != nil || done {
		return done, err
	}
	if err := a.executeToolCalls(ctx, sessionID, choice.Message.ToolCalls); err != nil {
		return false, err
	}
	return false, nil
}

// executeToolCalls runs each tool call sequentially and persists a tool turn
// per call. Returns a wrapped error if the store rejects any append.
func (a *Agent) executeToolCalls(ctx context.Context, sessionID string, calls []llm.ToolCall) error {
	for _, tc := range calls {
		toolResult, toolErr := a.tools.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
		if toolErr != nil {
			toolResult = fmt.Sprintf("error: %s", toolErr.Error())
		}
		if _, err := a.store.AppendTurn(ctx, sessionID, RoleTool, toolResult, nil, tc.ID, 0, 0); err != nil {
			return fmt.Errorf("append tool turn: %w", err)
		}
	}
	return nil
}

// checkRunTermination returns ErrTimeout when ctx is done and ErrBudgetExhaust
// when the in+out token sum is greater than MaxTokens. iter and maxIter are
// reserved for the future iterations-overflow guard.
func checkRunTermination(ctx context.Context, r *RunResult, iter, maxTokens, maxIter int) error {
	if ctx.Err() != nil {
		r.Err = ErrTimeout
		return ErrTimeout
	}
	if r.TokensIn+r.TokensOut > maxTokens {
		r.Err = ErrBudgetExhaust
		return ErrBudgetExhaust
	}
	return nil
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
func (a *Agent) invokeModel(ctx context.Context, sessionID string, messages []llm.Message, iter int) (*llm.CompletionResponse, error) {
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
// payload. Returns a wrapped error if the store rejects the append.
func (a *Agent) recordAssistantTurn(ctx context.Context, sessionID string, choice *llm.Choice) error {
	var toolCallsJSON json.RawMessage
	if len(choice.Message.ToolCalls) > 0 {
		toolCallsJSON, _ = json.Marshal(choice.Message.ToolCalls)
	}
	_, err := a.store.AppendTurn(ctx, sessionID, RoleAssistant, choice.Message.Content,
		toolCallsJSON, "", 0, 0)
	return err
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
