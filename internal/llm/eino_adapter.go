package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// EinoModelAdapter bridges an llm.Provider to eino's ToolCallingChatModel.
type EinoModelAdapter struct {
	provider Provider
	tools    []*schema.ToolInfo
}

var _ model.ToolCallingChatModel = (*EinoModelAdapter)(nil)

// NewEinoAdapter wraps an llm.Provider as an eino ToolCallingChatModel.
func NewEinoAdapter(p Provider) model.ToolCallingChatModel {
	return &EinoModelAdapter{provider: p}
}

// WithTools returns a new adapter with the given tools bound, without mutating
// the receiver.
func (a *EinoModelAdapter) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cp := make([]*schema.ToolInfo, len(tools))
	copy(cp, tools)
	return &EinoModelAdapter{
		provider: a.provider,
		tools:    cp,
	}, nil
}

// Generate converts eino messages to our format, calls the provider, and
// converts the response back.
func (a *EinoModelAdapter) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	commonOpts := model.GetCommonOptions(nil, opts...)

	msgs := make([]Message, 0, len(input))
	for _, m := range input {
		msgs = append(msgs, fromEinoMessage(m))
	}

	tools, err := a.resolveTools(commonOpts)
	if err != nil {
		return nil, fmt.Errorf("eino adapter: resolve tools: %w", err)
	}

	req := CompletionRequest{
		Messages: msgs,
		Tools:    tools,
	}

	if commonOpts.Model != nil {
		req.Model = *commonOpts.Model
	}
	if commonOpts.Temperature != nil {
		t := float64(*commonOpts.Temperature)
		req.Temperature = &t
	}
	if commonOpts.MaxTokens != nil {
		req.MaxTokens = commonOpts.MaxTokens
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("eino adapter: provider.Complete: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("eino adapter: empty choices in response")
	}

	return toEinoMessage(&resp.Choices[0].Message, &resp.Usage), nil
}

// Stream wraps Generate in a single-element stream. A real streaming
// implementation can be added when the provider supports SSE.
func (a *EinoModelAdapter) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := a.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (a *EinoModelAdapter) resolveTools(opts *model.Options) ([]Tool, error) {
	toolInfos := a.tools
	if opts != nil && len(opts.Tools) > 0 {
		toolInfos = opts.Tools
	}
	if len(toolInfos) == 0 {
		return nil, nil
	}

	result := make([]Tool, 0, len(toolInfos))
	for _, ti := range toolInfos {
		tool, err := toolInfoToLLMTool(ti)
		if err != nil {
			return nil, err
		}
		result = append(result, tool)
	}
	return result, nil
}

func toolInfoToLLMTool(ti *schema.ToolInfo) (Tool, error) {
	var params json.RawMessage
	//nolint:staticcheck // QF1008: ParamsOneOf is an embedded field on upstream schema.ToolInfo; keep the explicit selector for readability when the field is unset in older callers.
	js, err := ti.ParamsOneOf.ToJSONSchema()
	if err != nil {
		return Tool{}, fmt.Errorf("tool %q: params to jsonschema: %w", ti.Name, err)
	}
	if js != nil {
		b, err := json.Marshal(js)
		if err != nil {
			return Tool{}, fmt.Errorf("tool %q: marshal jsonschema: %w", ti.Name, err)
		}
		params = b
	}
	return Tool{
		Type: "function",
		Function: FunctionDef{
			Name:        ti.Name,
			Description: ti.Desc,
			Parameters:  params,
		},
	}, nil
}

// fromEinoMessage converts an eino schema.Message to our llm.Message.
func fromEinoMessage(m *schema.Message) Message {
	msg := Message{
		Role:       string(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return msg
}

// toEinoMessage converts our llm.Message (plus usage) back to an eino schema.Message.
func toEinoMessage(m *Message, usage *Usage) *schema.Message {
	msg := &schema.Message{
		Role:    schema.RoleType(m.Role),
		Content: m.Content,
	}
	if m.Reasoning != "" {
		msg.ReasoningContent = m.Reasoning
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: schema.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	if usage != nil {
		msg.ResponseMeta = &schema.ResponseMeta{
			Usage: &schema.TokenUsage{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
			},
		}
	}
	return msg
}
