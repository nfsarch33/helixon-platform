package llm

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEinoAdapter_ImplementsToolCallingChatModel(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewEinoAdapter(mock)

	//nolint:staticcheck // QF1011: explicit type documents the interface contract under test.
	var _ model.ToolCallingChatModel = adapter
}

func TestEinoAdapter_Generate_TextOnly(t *testing.T) {
	mock := NewMockProvider()
	mock.DefaultResp = &CompletionResponse{
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "Hello from the adapter",
				},
			},
		},
		Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}

	adapter := NewEinoAdapter(mock)
	ctx := context.Background()
	input := []*schema.Message{
		schema.UserMessage("Hi there"),
	}

	result, err := adapter.Generate(ctx, input)
	require.NoError(t, err)

	assert.Equal(t, schema.Assistant, result.Role)
	assert.Equal(t, "Hello from the adapter", result.Content)

	require.Len(t, mock.Requests, 1)
	assert.Equal(t, "user", mock.Requests[0].Messages[0].Role)
	assert.Equal(t, "Hi there", mock.Requests[0].Messages[0].Content)
}

func TestEinoAdapter_Generate_WithToolCalls(t *testing.T) {
	mock := NewMockProvider()
	mock.DefaultResp = &CompletionResponse{
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: FunctionCall{
								Name:      "click",
								Arguments: `{"selector": "#submit"}`,
							},
						},
					},
				},
			},
		},
	}

	adapter := NewEinoAdapter(mock)
	ctx := context.Background()
	input := []*schema.Message{
		schema.UserMessage("Click the submit button"),
	}

	result, err := adapter.Generate(ctx, input)
	require.NoError(t, err)

	assert.Equal(t, schema.Assistant, result.Role)
	require.Len(t, result.ToolCalls, 1)
	assert.Equal(t, "call_123", result.ToolCalls[0].ID)
	assert.Equal(t, "function", result.ToolCalls[0].Type)
	assert.Equal(t, "click", result.ToolCalls[0].Function.Name)
	assert.Equal(t, `{"selector": "#submit"}`, result.ToolCalls[0].Function.Arguments)
}

func TestEinoAdapter_WithTools(t *testing.T) {
	mock := NewMockProvider()
	mock.DefaultResp = &CompletionResponse{
		Choices: []Choice{
			{
				Index:   0,
				Message: Message{Role: "assistant", Content: "done"},
			},
		},
	}

	adapter := NewEinoAdapter(mock)

	tools := []*schema.ToolInfo{
		{
			Name: "click",
			Desc: "Click an element",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"selector": {Type: schema.String, Desc: "CSS selector", Required: true},
			}),
		},
	}

	withTools, err := adapter.WithTools(tools)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = withTools.Generate(ctx, []*schema.Message{
		schema.UserMessage("click the button"),
	})
	require.NoError(t, err)

	require.Len(t, mock.Requests, 1)
	req := mock.Requests[0]
	require.Len(t, req.Tools, 1)
	assert.Equal(t, "function", req.Tools[0].Type)
	assert.Equal(t, "click", req.Tools[0].Function.Name)
	assert.Equal(t, "Click an element", req.Tools[0].Function.Description)
	assert.Contains(t, string(req.Tools[0].Function.Parameters), `"selector"`)
}

func TestEinoAdapter_WithTools_Immutable(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewEinoAdapter(mock)

	tools := []*schema.ToolInfo{
		{Name: "search", Desc: "Search the web"},
	}

	withTools, err := adapter.WithTools(tools)
	require.NoError(t, err)

	original, ok := adapter.(*EinoModelAdapter)
	require.True(t, ok)
	assert.Empty(t, original.tools)

	derived, ok := withTools.(*EinoModelAdapter)
	require.True(t, ok)
	assert.Len(t, derived.tools, 1)
}

func TestEinoAdapter_MessageConversion_AllRoles(t *testing.T) {
	mock := NewMockProvider()
	mock.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "ok"}}},
	}

	adapter := NewEinoAdapter(mock)
	ctx := context.Background()

	input := []*schema.Message{
		schema.SystemMessage("You are helpful"),
		schema.UserMessage("Do something"),
		schema.AssistantMessage("I'll use a tool", []schema.ToolCall{
			{ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "click", Arguments: `{}`}},
		}),
		schema.ToolMessage(`{"result": "clicked"}`, "call_1"),
	}

	_, err := adapter.Generate(ctx, input)
	require.NoError(t, err)

	require.Len(t, mock.Requests, 1)
	msgs := mock.Requests[0].Messages

	assert.Equal(t, "system", msgs[0].Role)
	assert.Equal(t, "You are helpful", msgs[0].Content)

	assert.Equal(t, "user", msgs[1].Role)
	assert.Equal(t, "Do something", msgs[1].Content)

	assert.Equal(t, "assistant", msgs[2].Role)
	require.Len(t, msgs[2].ToolCalls, 1)
	assert.Equal(t, "call_1", msgs[2].ToolCalls[0].ID)

	assert.Equal(t, "tool", msgs[3].Role)
	assert.Equal(t, "call_1", msgs[3].ToolCallID)
}

func TestEinoAdapter_RoundTrip_ToolCallFlow(t *testing.T) {
	mock := NewMockProvider()
	mock.DefaultResp = &CompletionResponse{
		Choices: []Choice{
			{
				Message: Message{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{
							ID:   "call_xyz",
							Type: "function",
							Function: FunctionCall{
								Name:      "observe",
								Arguments: `{}`,
							},
						},
					},
				},
			},
		},
	}

	adapter := NewEinoAdapter(mock)

	tools := []*schema.ToolInfo{
		{Name: "observe", Desc: "Take a screenshot"},
	}
	withTools, err := adapter.WithTools(tools)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := withTools.Generate(ctx, []*schema.Message{
		schema.UserMessage("What do you see?"),
	})
	require.NoError(t, err)

	assert.Equal(t, schema.Assistant, result.Role)
	require.Len(t, result.ToolCalls, 1)
	assert.Equal(t, "call_xyz", result.ToolCalls[0].ID)
	assert.Equal(t, "observe", result.ToolCalls[0].Function.Name)

	require.Len(t, mock.Requests, 1)
	assert.Len(t, mock.Requests[0].Tools, 1)
	assert.Equal(t, "observe", mock.Requests[0].Tools[0].Function.Name)
}
