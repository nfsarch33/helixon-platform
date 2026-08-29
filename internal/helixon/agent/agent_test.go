package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

type mockProvider struct {
	responses []*llm.CompletionResponse
	callIdx   int
}

func (m *mockProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if m.callIdx >= len(m.responses) {
		return nil, fmt.Errorf("no more mock responses")
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

type mockToolExecutor struct {
	tools   []llm.Tool
	results map[string]string
}

func (m *mockToolExecutor) Execute(_ context.Context, name string, _ string) (string, error) {
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return "", fmt.Errorf("tool %s not found", name)
}

func (m *mockToolExecutor) Available() []llm.Tool {
	return m.tools
}

func newTestAgent(t *testing.T, provider llm.Provider, tools ToolExecutor) (*Agent, *SessionStore) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "agent_test.db")
	store, err := NewSessionStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	agent := New(provider, tools, store, Config{
		MaxIterations: 10,
		MaxTokens:     50000,
		Timeout:       10 * time.Second,
		SystemPrompt:  "You are a helpful assistant.",
	})
	return agent, store
}

func TestAgentDirectResponse(t *testing.T) {
	provider := &mockProvider{
		responses: []*llm.CompletionResponse{
			{
				Choices: []llm.Choice{{
					Message: llm.Message{Role: "assistant", Content: "Hello! How can I help?"},
				}},
				Usage: llm.Usage{PromptTokens: 15, CompletionTokens: 8, TotalTokens: 23},
			},
		},
	}
	tools := &mockToolExecutor{}

	agent, store := newTestAgent(t, provider, tools)
	sess, err := store.CreateSession(context.Background(), "test", nil)
	require.NoError(t, err)

	result, err := agent.Run(context.Background(), sess.ID, "Hi there")
	require.NoError(t, err)
	assert.Equal(t, "Hello! How can I help?", result.FinalContent)
	assert.Equal(t, 1, result.Iterations)
	assert.Equal(t, 15, result.TokensIn)
	assert.Equal(t, 8, result.TokensOut)
}

func TestAgentToolCallLoop(t *testing.T) {
	searchTC := llm.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "search",
			Arguments: `{"query":"test"}`,
		},
	}

	provider := &mockProvider{
		responses: []*llm.CompletionResponse{
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []llm.ToolCall{searchTC},
					},
				}},
				Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 10},
			},
			{
				Choices: []llm.Choice{{
					Message: llm.Message{Role: "assistant", Content: "Based on the search: the answer is 42."},
				}},
				Usage: llm.Usage{PromptTokens: 30, CompletionTokens: 12},
			},
		},
	}

	tools := &mockToolExecutor{
		tools: []llm.Tool{{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        "search",
				Description: "Search for information",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
			},
		}},
		results: map[string]string{
			"search": "The answer to everything is 42.",
		},
	}

	agent, store := newTestAgent(t, provider, tools)
	sess, err := store.CreateSession(context.Background(), "test", nil)
	require.NoError(t, err)

	result, err := agent.Run(context.Background(), sess.ID, "What is the answer?")
	require.NoError(t, err)
	assert.Equal(t, "Based on the search: the answer is 42.", result.FinalContent)
	assert.Equal(t, 2, result.Iterations)
	assert.Equal(t, 50, result.TokensIn)
	assert.Equal(t, 22, result.TokensOut)

	turns, err := store.ListTurns(context.Background(), sess.ID, 0)
	require.NoError(t, err)
	assert.Len(t, turns, 4)
	assert.Equal(t, RoleUser, turns[0].Role)
	assert.Equal(t, RoleAssistant, turns[1].Role)
	assert.Equal(t, RoleTool, turns[2].Role)
	assert.Equal(t, "call_1", turns[2].ToolCallID)
	assert.Equal(t, RoleAssistant, turns[3].Role)
}

func TestAgentMaxIterations(t *testing.T) {
	infiniteTC := llm.ToolCall{
		ID: "call_loop", Type: "function",
		Function: llm.FunctionCall{Name: "noop", Arguments: "{}"},
	}
	resp := &llm.CompletionResponse{
		Choices: []llm.Choice{{
			Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{infiniteTC}},
		}},
		Usage: llm.Usage{PromptTokens: 5, CompletionTokens: 5},
	}

	responses := make([]*llm.CompletionResponse, 20)
	for i := range responses {
		responses[i] = resp
	}
	provider := &mockProvider{responses: responses}

	tools := &mockToolExecutor{
		tools: []llm.Tool{{
			Type:     "function",
			Function: llm.FunctionDef{Name: "noop", Description: "does nothing"},
		}},
		results: map[string]string{"noop": "ok"},
	}

	dsn := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSessionStore(context.Background(), dsn)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	agent := New(provider, tools, store, Config{
		MaxIterations: 3,
		MaxTokens:     100000,
		Timeout:       10 * time.Second,
	})

	sess, err := store.CreateSession(context.Background(), "test", nil)
	require.NoError(t, err)

	result, err := agent.Run(context.Background(), sess.ID, "loop forever")
	assert.ErrorIs(t, err, ErrMaxIterations)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.Iterations, "the loop must stop AT MaxIterations, not near it")

	// The iteration cap is what stopped this run, so the store must hold
	// exactly the turns those iterations produced: one user turn, then an
	// assistant turn and a tool turn per iteration. Asserting the count keeps
	// the test honest about WHY it passed — a run that died early on a slow
	// durable write would satisfy ErrMaxIterations only by accident, and this
	// catches that without reference to any clock.
	turns, err := store.ListTurns(context.Background(), sess.ID, 0)
	require.NoError(t, err)
	// len(), not assert.Len: a mismatch here should print two numbers, not
	// dump every persisted turn.
	assert.Equal(t, 1+2*3, len(turns),
		"expected the user turn plus an assistant and a tool turn per iteration")
}

func TestAgentBudgetExhaust(t *testing.T) {
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
	provider := &mockProvider{responses: responses}
	tools := &mockToolExecutor{
		tools:   []llm.Tool{{Type: "function", Function: llm.FunctionDef{Name: "noop"}}},
		results: map[string]string{"noop": "ok"},
	}

	dsn := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSessionStore(context.Background(), dsn)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	agent := New(provider, tools, store, Config{
		MaxIterations: 20,
		MaxTokens:     15000,
		Timeout:       10 * time.Second,
	})

	sess, err := store.CreateSession(context.Background(), "test", nil)
	require.NoError(t, err)

	result, err := agent.Run(context.Background(), sess.ID, "expensive query")
	assert.ErrorIs(t, err, ErrBudgetExhaust)
	require.NotNil(t, result)
	assert.Greater(t, result.TokensIn+result.TokensOut, 15000,
		"the run must actually have exceeded the budget it reports exhausting")

	// The budget verdict lands on the second model response: that response is
	// recorded (the provider billed for it — see
	// TestBudgetExhaustStillAccountsForTheFinalCall) and then its tool calls
	// are refused. So the store holds the user turn, the first iteration's
	// assistant and tool turns, and the assistant turn that blew the budget —
	// but no tool turn for it.
	//
	// This is the load-independent form of the assertion. The count is decided
	// by control flow, not by how fast the host can fsync, and it fails if the
	// budget check is moved back to the top of the next iteration, which would
	// leave 5 turns with a further round of tool side effects already done.
	turns, err := store.ListTurns(context.Background(), sess.ID, 0)
	require.NoError(t, err)
	// len(), not require.Len: a mismatch here should print two numbers, not
	// dump every persisted turn.
	require.Equal(t, 4, len(turns),
		"expected user, assistant, tool, and the assistant turn that exhausted the budget")
	assert.Equal(t, RoleUser, turns[0].Role)
	assert.Equal(t, RoleAssistant, turns[1].Role)
	assert.Equal(t, RoleTool, turns[2].Role)
	assert.Equal(t, RoleAssistant, turns[3].Role,
		"the budget-exhausting response must be recorded, not discarded")
}
