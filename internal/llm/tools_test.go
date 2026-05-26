package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTool_JSONRoundTrip(t *testing.T) {
	tool := Tool{
		Type: "function",
		Function: FunctionDef{
			Name:        "get_weather",
			Description: "Get the current weather for a location",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"location": {"type": "string", "description": "City name"},
					"unit": {"type": "string", "enum": ["celsius", "fahrenheit"]}
				},
				"required": ["location"]
			}`),
		},
	}

	data, err := json.Marshal(tool)
	require.NoError(t, err)

	var decoded Tool
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "function", decoded.Type)
	assert.Equal(t, "get_weather", decoded.Function.Name)
	assert.Equal(t, "Get the current weather for a location", decoded.Function.Description)
	assert.Contains(t, string(decoded.Function.Parameters), `"location"`)
}

func TestToolCall_JSONRoundTrip(t *testing.T) {
	tc := ToolCall{
		ID:   "call_abc123",
		Type: "function",
		Function: FunctionCall{
			Name:      "get_weather",
			Arguments: `{"location": "London", "unit": "celsius"}`,
		},
	}

	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var decoded ToolCall
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "call_abc123", decoded.ID)
	assert.Equal(t, "function", decoded.Type)
	assert.Equal(t, "get_weather", decoded.Function.Name)
	assert.Equal(t, `{"location": "London", "unit": "celsius"}`, decoded.Function.Arguments)
}

func TestToolChoice_String(t *testing.T) {
	tc := ToolChoiceAuto
	assert.Equal(t, "auto", string(tc))

	tc = ToolChoiceNone
	assert.Equal(t, "none", string(tc))

	tc = ToolChoiceRequired
	assert.Equal(t, "required", string(tc))
}

func TestToolChoice_MarshalJSON_String(t *testing.T) {
	tc := ToolChoiceAuto
	data, err := json.Marshal(tc)
	require.NoError(t, err)
	assert.Equal(t, `"auto"`, string(data))
}

func TestTool_EmptyParameters(t *testing.T) {
	tool := Tool{
		Type: "function",
		Function: FunctionDef{
			Name:        "done",
			Description: "Signal task completion",
		},
	}

	data, err := json.Marshal(tool)
	require.NoError(t, err)

	var decoded Tool
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "done", decoded.Function.Name)
	assert.Nil(t, decoded.Function.Parameters)
}

func TestCompletionRequest_WithTools(t *testing.T) {
	tools := []Tool{
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "search",
				Description: "Search the web",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
			},
		},
	}
	tc := ToolChoiceAuto
	req := CompletionRequest{
		Model: "gpt-4.1-mini",
		Messages: []Message{
			{Role: "user", Content: "search for cats"},
		},
		Tools:      tools,
		ToolChoice: &tc,
	}

	assert.Len(t, req.Tools, 1)
	assert.Equal(t, "search", req.Tools[0].Function.Name)
	assert.NotNil(t, req.ToolChoice)
	assert.Equal(t, ToolChoiceAuto, *req.ToolChoice)
}

func TestMessage_WithToolCalls(t *testing.T) {
	msg := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: FunctionCall{
					Name:      "click",
					Arguments: `{"selector": "#btn"}`,
				},
			},
			{
				ID:   "call_2",
				Type: "function",
				Function: FunctionCall{
					Name:      "observe",
					Arguments: `{}`,
				},
			},
		},
	}

	assert.Len(t, msg.ToolCalls, 2)
	assert.Equal(t, "click", msg.ToolCalls[0].Function.Name)
	assert.Equal(t, "observe", msg.ToolCalls[1].Function.Name)

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded Message
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Len(t, decoded.ToolCalls, 2)
	assert.Equal(t, "call_1", decoded.ToolCalls[0].ID)
}
