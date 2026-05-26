package helixon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// sequentialProvider returns responses in order. This lets us script
// multi-turn conversations: first call returns a tool call, second
// call returns a text response, etc.
type sequentialProvider struct {
	responses []*llm.CompletionResponse
	idx       int
}

func (p *sequentialProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if p.idx >= len(p.responses) {
		return nil, fmt.Errorf("mock: no more responses (called %d times)", p.idx+1)
	}
	r := p.responses[p.idx]
	p.idx++
	return r, nil
}

// TestE2E_PromptToolCallResponse exercises the full happy path:
// user prompt -> LLM requests shell tool -> tool executes -> LLM produces final text.
func TestE2E_PromptToolCallResponse(t *testing.T) {
	shellToolCall := llm.ToolCall{
		ID:   "call_shell_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "shell",
			Arguments: `{"command":"echo","args":["hello from shell"]}`,
		},
	}

	provider := &sequentialProvider{
		responses: []*llm.CompletionResponse{
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []llm.ToolCall{shellToolCall},
					},
				}},
				Usage: llm.Usage{PromptTokens: 25, CompletionTokens: 15},
			},
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:    "assistant",
						Content: "The shell command returned: hello from shell",
					},
				}},
				Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 12},
			},
		},
	}

	reg := tooldispatch.NewRegistry(nil)
	require.NoError(t, reg.Register(builtins.ShellTool(builtins.ShellConfig{
		AllowedCommands: []string{"echo", "ls", "pwd"},
		Timeout:         5 * time.Second,
	})))

	dsn := filepath.Join(t.TempDir(), "e2e.db")
	store, err := agent.NewSessionStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ag := agent.New(provider, reg, store, agent.Config{
		MaxIterations: 10,
		MaxTokens:     100000,
		Timeout:       30 * time.Second,
		SystemPrompt:  "You are a helpful assistant with shell access.",
	})

	sess, err := store.CreateSession(context.Background(), "e2e-test", nil)
	require.NoError(t, err)

	result, err := ag.Run(context.Background(), sess.ID, "List files in /tmp")
	require.NoError(t, err)

	assert.Equal(t, "The shell command returned: hello from shell", result.FinalContent)
	assert.Equal(t, 2, result.Iterations)
	assert.Equal(t, 65, result.TokensIn)
	assert.Equal(t, 27, result.TokensOut)

	turns, err := store.ListTurns(context.Background(), sess.ID, 0)
	require.NoError(t, err)
	assert.Len(t, turns, 4) // user, assistant+tool_call, tool_result, assistant_final

	assert.Equal(t, agent.RoleUser, turns[0].Role)
	assert.Equal(t, "List files in /tmp", turns[0].Content)

	assert.Equal(t, agent.RoleAssistant, turns[1].Role)
	assert.NotEmpty(t, turns[1].ToolCalls)

	assert.Equal(t, agent.RoleTool, turns[2].Role)
	assert.Equal(t, "call_shell_1", turns[2].ToolCallID)
	assert.Contains(t, turns[2].Content, "hello from shell")

	assert.Equal(t, agent.RoleAssistant, turns[3].Role)
	assert.Equal(t, "The shell command returned: hello from shell", turns[3].Content)
}

// TestE2E_FileReadWriteRoundTrip exercises file_read and file_write tools.
func TestE2E_FileReadWriteRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "test.txt")

	writeToolCall := llm.ToolCall{
		ID:   "call_write_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "file_write",
			Arguments: fmt.Sprintf(`{"path":%q,"content":"hello from helixon"}`, targetFile),
		},
	}

	readToolCall := llm.ToolCall{
		ID:   "call_read_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "file_read",
			Arguments: fmt.Sprintf(`{"path":%q}`, targetFile),
		},
	}

	provider := &sequentialProvider{
		responses: []*llm.CompletionResponse{
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []llm.ToolCall{writeToolCall},
					},
				}},
				Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 10},
			},
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []llm.ToolCall{readToolCall},
					},
				}},
				Usage: llm.Usage{PromptTokens: 30, CompletionTokens: 10},
			},
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:    "assistant",
						Content: "I wrote and then read back: hello from helixon",
					},
				}},
				Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 12},
			},
		},
	}

	reg := tooldispatch.NewRegistry(nil)
	require.NoError(t, reg.Register(builtins.FileWriteTool(builtins.FileWriteConfig{})))
	require.NoError(t, reg.Register(builtins.FileReadTool(builtins.FileReadConfig{})))

	dsn := filepath.Join(t.TempDir(), "e2e_file.db")
	store, err := agent.NewSessionStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ag := agent.New(provider, reg, store, agent.Config{
		MaxIterations: 10,
		MaxTokens:     100000,
		Timeout:       30 * time.Second,
	})

	sess, err := store.CreateSession(context.Background(), "e2e-file", nil)
	require.NoError(t, err)

	result, err := ag.Run(context.Background(), sess.ID, "Write a file and read it back")
	require.NoError(t, err)

	assert.Equal(t, "I wrote and then read back: hello from helixon", result.FinalContent)
	assert.Equal(t, 3, result.Iterations)
}

// TestE2E_MultipleToolCalls verifies the agent handles multiple tool calls
// in a single assistant response.
func TestE2E_MultipleToolCalls(t *testing.T) {
	tc1 := llm.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "shell",
			Arguments: `{"command":"echo","args":["first"]}`,
		},
	}
	tc2 := llm.ToolCall{
		ID:   "call_2",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "shell",
			Arguments: `{"command":"echo","args":["second"]}`,
		},
	}

	provider := &sequentialProvider{
		responses: []*llm.CompletionResponse{
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []llm.ToolCall{tc1, tc2},
					},
				}},
				Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 20},
			},
			{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Both commands completed successfully.",
					},
				}},
				Usage: llm.Usage{PromptTokens: 50, CompletionTokens: 8},
			},
		},
	}

	reg := tooldispatch.NewRegistry(nil)
	require.NoError(t, reg.Register(builtins.ShellTool(builtins.ShellConfig{
		AllowedCommands: []string{"echo"},
	})))

	dsn := filepath.Join(t.TempDir(), "e2e_multi.db")
	store, err := agent.NewSessionStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ag := agent.New(provider, reg, store, agent.Config{
		MaxIterations: 10,
		MaxTokens:     100000,
		Timeout:       30 * time.Second,
	})

	sess, err := store.CreateSession(context.Background(), "e2e-multi", nil)
	require.NoError(t, err)

	result, err := ag.Run(context.Background(), sess.ID, "Run two echo commands")
	require.NoError(t, err)

	assert.Equal(t, "Both commands completed successfully.", result.FinalContent)
	assert.Equal(t, 2, result.Iterations)

	turns, err := store.ListTurns(context.Background(), sess.ID, 0)
	require.NoError(t, err)
	// user + assistant(2 tool calls) + tool_result_1 + tool_result_2 + assistant_final = 5
	assert.Len(t, turns, 5)

	toolTurns := 0
	for _, turn := range turns {
		if turn.Role == agent.RoleTool {
			toolTurns++
		}
	}
	assert.Equal(t, 2, toolTurns)
}

// TestE2E_ToolSchemaAvailableToLLM verifies that registered tools are
// passed to the LLM provider in the Available() list.
func TestE2E_ToolSchemaAvailableToLLM(t *testing.T) {
	reg := tooldispatch.NewRegistry(nil)

	require.NoError(t, builtins.RegisterAll(reg, builtins.Options{
		Shell:     &builtins.ShellConfig{AllowedCommands: []string{"echo"}},
		FileRead:  &builtins.FileReadConfig{},
		FileWrite: &builtins.FileWriteConfig{},
	}))

	available := reg.Available()
	assert.Len(t, available, 3)

	names := make(map[string]bool)
	for _, tool := range available {
		names[tool.Function.Name] = true
		assert.Equal(t, "function", tool.Type)
		assert.NotEmpty(t, tool.Function.Description)

		var schema map[string]any
		require.NoError(t, json.Unmarshal(tool.Function.Parameters, &schema))
		assert.Equal(t, "object", schema["type"])
	}

	assert.True(t, names["shell"])
	assert.True(t, names["file_read"])
	assert.True(t, names["file_write"])
}
