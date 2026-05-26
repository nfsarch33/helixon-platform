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
func (a *Agent) Run(ctx context.Context, sessionID, userMessage string) (*RunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()

	_, err := a.store.AppendTurn(ctx, sessionID, RoleUser, userMessage, nil, "", 0, 0)
	if err != nil {
		return nil, fmt.Errorf("append user turn: %w", err)
	}

	result := &RunResult{SessionID: sessionID}

	for iter := 0; iter < a.cfg.MaxIterations; iter++ {
		result.Iterations = iter + 1

		if ctx.Err() != nil {
			result.Err = ErrTimeout
			return result, ErrTimeout
		}

		if result.TokensIn+result.TokensOut > a.cfg.MaxTokens {
			result.Err = ErrBudgetExhaust
			return result, ErrBudgetExhaust
		}

		messages, err := a.buildMessages(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("build messages: %w", err)
		}

		handler := callbacks.HandlerFromContext(ctx)
		if handler != nil {
			info := &callbacks.RunInfo{
				ComponentName: "helixon.agent",
				RunID:         sessionID,
				StartedAt:     time.Now(),
				Tags:          map[string]string{"iteration": fmt.Sprintf("%d", iter+1)},
			}
			ctx = handler.OnStart(ctx, info, userMessage)
		}

		req := llm.CompletionRequest{
			Messages: messages,
			Tools:    a.tools.Available(),
		}

		resp, err := a.provider.Complete(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("llm complete (iter %d): %w", iter+1, err)
		}

		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("empty response at iteration %d", iter+1)
		}

		choice := resp.Choices[0]
		result.TokensIn += resp.Usage.PromptTokens
		result.TokensOut += resp.Usage.CompletionTokens

		a.logger.Debug("agent iteration",
			slog.Int("iteration", iter+1),
			slog.Int("tool_calls", len(choice.Message.ToolCalls)),
			slog.Int("tokens_in", resp.Usage.PromptTokens),
			slog.Int("tokens_out", resp.Usage.CompletionTokens),
		)

		var toolCallsJSON json.RawMessage
		if len(choice.Message.ToolCalls) > 0 {
			toolCallsJSON, _ = json.Marshal(choice.Message.ToolCalls)
		}

		_, err = a.store.AppendTurn(ctx, sessionID, RoleAssistant, choice.Message.Content,
			toolCallsJSON, "", resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
		if err != nil {
			return nil, fmt.Errorf("append assistant turn: %w", err)
		}

		if len(choice.Message.ToolCalls) == 0 {
			result.FinalContent = choice.Message.Content
			return result, nil
		}

		for _, tc := range choice.Message.ToolCalls {
			toolResult, toolErr := a.tools.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
			if toolErr != nil {
				toolResult = fmt.Sprintf("error: %s", toolErr.Error())
			}

			_, err = a.store.AppendTurn(ctx, sessionID, RoleTool, toolResult, nil, tc.ID, 0, 0)
			if err != nil {
				return nil, fmt.Errorf("append tool turn: %w", err)
			}
		}
	}

	result.Err = ErrMaxIterations
	return result, ErrMaxIterations
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
