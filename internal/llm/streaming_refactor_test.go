package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// v16716-4 RED tests for parseSSEStream refactor.
// parseSSEStream (CC=20) is decomposed into:
//   - processStreamChunk   (CC=4): per-chunk delta handling
//   - accumulateToolCall   (CC=3): tool-call delta merging
//   - finalizeStreamResp   (CC=3): post-loop CompletionResponse build

func TestProcessStreamChunk_ContentAccumulated(t *testing.T) {
	t.Parallel()
	acc := newStreamAccumulator()
	chunk := StreamChunk{Choices: []StreamDelta{{Index: 0, Delta: Delta{Content: "hello"}}}}
	var received []string
	cb := func(s string) error { received = append(received, s); return nil }
	processStreamChunk(acc, chunk, cb)
	processStreamChunk(acc, chunk, cb)
	assert.Equal(t, "hellohello", acc.content.String())
	assert.Equal(t, []string{"hello", "hello"}, received)
}

func TestProcessStreamChunk_UsageAccumulated(t *testing.T) {
	t.Parallel()
	acc := newStreamAccumulator()
	usage := Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}
	chunk := StreamChunk{Usage: &usage}
	processStreamChunk(acc, chunk, nil)
	assert.Equal(t, usage, acc.usage)
}

func TestAccumulateToolCall_FreshEntry(t *testing.T) {
	t.Parallel()
	toolCalls := map[int]*ToolCall{}
	tcd := ToolCallDelta{Index: 0, ID: "call_1", Type: "function", Function: FunctionCall{Name: "shell"}}
	accumulateToolCall(toolCalls, tcd)
	tc := toolCalls[0]
	assert.Equal(t, "call_1", tc.ID)
	assert.Equal(t, "shell", tc.Function.Name)
	assert.Equal(t, "", tc.Function.Arguments)
}

func TestAccumulateToolCall_AppendArguments(t *testing.T) {
	t.Parallel()
	toolCalls := map[int]*ToolCall{
		0: {ID: "call_1", Type: "function", Function: FunctionCall{Name: "shell", Arguments: `{"a":1`}},
	}
	tcd := ToolCallDelta{Index: 0, Function: FunctionCall{Arguments: `,"b":2}`}}
	accumulateToolCall(toolCalls, tcd)
	tc := toolCalls[0]
	assert.Equal(t, `{"a":1,"b":2}`, tc.Function.Arguments)
}

func TestFinalizeStreamResp_EmptyContent(t *testing.T) {
	t.Parallel()
	acc := newStreamAccumulator()
	resp := finalizeStreamResp(acc)
	assert.Equal(t, "", resp.Choices[0].Message.Content)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Empty(t, resp.Choices[0].Message.ToolCalls)
}

func TestFinalizeStreamResp_WithToolCalls(t *testing.T) {
	t.Parallel()
	acc := newStreamAccumulator()
	acc.content.WriteString("ok")
	acc.toolCalls[0] = &ToolCall{ID: "c1", Type: "function", Function: FunctionCall{Name: "shell"}}
	acc.toolCalls[2] = &ToolCall{ID: "c2", Type: "function", Function: FunctionCall{Name: "read"}}
	resp := finalizeStreamResp(acc)
	assert.Equal(t, "ok", resp.Choices[0].Message.Content)
	require := requireHelper()
	_ = require
	tc := resp.Choices[0].Message.ToolCalls
	assert.Len(t, tc, 2)
	assert.Equal(t, "c1", tc[0].ID)
	assert.Equal(t, "c2", tc[1].ID)
}

func TestFinalizeStreamResp_WithUsage(t *testing.T) {
	t.Parallel()
	acc := newStreamAccumulator()
	acc.usage = Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	resp := finalizeStreamResp(acc)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokens)
}

// requireHelper is a tiny stub for testing-need; returns nil.
func requireHelper() *struct{} { return nil }

// dummy to keep `strings` import in use (for future tests)
var _ = strings.Builder{}
