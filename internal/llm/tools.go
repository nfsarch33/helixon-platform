package llm

import "encoding/json"

// Tool represents a tool available to the model, matching the OpenAI tools API.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a function the model may call.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the name and arguments of a function invocation.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolChoice controls how the model selects tools.
type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceRequired ToolChoice = "required"
)
