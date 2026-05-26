package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mockExec(output []byte, err error) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return output, err
	}
}

func mockExecCapture(output []byte, err error, capturedArgs *[]string) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*capturedArgs = append([]string{name}, args...)
		return output, err
	}
}

func TestClaudeCLI_Complete_JSONResponse(t *testing.T) {
	cliResp := claudeCLIResponse{
		Result: "Hello from Claude",
		Model:  "claude-sonnet-4-20250514",
		Usage: struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		}{InputTokens: 10, OutputTokens: 20},
		CostUSD: 0.001,
	}
	data, _ := json.Marshal(cliResp)

	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	client.execFunc = mockExec(data, nil)

	resp, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Hello from Claude", resp.Choices[0].Message.Content)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 20, resp.Usage.CompletionTokens)
	assert.Equal(t, 30, resp.Usage.TotalTokens)
}

func TestClaudeCLI_Complete_PlainTextResponse(t *testing.T) {
	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	client.execFunc = mockExec([]byte("Just plain text response"), nil)

	resp, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Just plain text response", resp.Choices[0].Message.Content)
}

func TestClaudeCLI_Complete_CLIError(t *testing.T) {
	cliResp := claudeCLIResponse{
		Result:  "Something went wrong",
		IsError: true,
	}
	data, _ := json.Marshal(cliResp)

	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	client.execFunc = mockExec(data, nil)

	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Something went wrong")
}

func TestClaudeCLI_Complete_ExecFailure(t *testing.T) {
	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	client.execFunc = mockExec(nil, errors.New("command not found"))

	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLLMClient)
}

func TestClaudeCLI_Complete_EmptyMessages(t *testing.T) {
	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	_, err := client.Complete(context.Background(), CompletionRequest{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyMessages)
}

func TestClaudeCLI_Complete_EmptyResponse(t *testing.T) {
	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	client.execFunc = mockExec([]byte(""), nil)

	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

func TestClaudeCLI_Complete_PassesModelArg(t *testing.T) {
	var captured []string
	data, _ := json.Marshal(claudeCLIResponse{Result: "ok"})

	client := NewClaudeCLIClient(ClaudeCLIConfig{Model: "default-model"})
	client.execFunc = mockExecCapture(data, nil, &captured)

	_, err := client.Complete(context.Background(), CompletionRequest{
		Model:    "override-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	assert.Contains(t, captured, "--model")
	assert.Contains(t, captured, "override-model")
}

func TestClaudeCLI_Complete_ConfigModel(t *testing.T) {
	var captured []string
	data, _ := json.Marshal(claudeCLIResponse{Result: "ok"})

	client := NewClaudeCLIClient(ClaudeCLIConfig{Model: "config-model"})
	client.execFunc = mockExecCapture(data, nil, &captured)

	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	assert.Contains(t, captured, "--model")
	assert.Contains(t, captured, "config-model")
}

func TestClaudeCLI_Complete_NoModelOmitsFlag(t *testing.T) {
	var captured []string
	data, _ := json.Marshal(claudeCLIResponse{Result: "ok"})

	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	client.execFunc = mockExecCapture(data, nil, &captured)

	_, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	for _, arg := range captured {
		assert.NotEqual(t, "--model", arg)
	}
}

func TestClaudeCLI_DefaultConfig(t *testing.T) {
	client := NewClaudeCLIClient(ClaudeCLIConfig{})
	assert.Equal(t, "claude", client.binary)
	assert.Equal(t, 5*time.Minute, client.timeout)
}

func TestClaudeCLI_CustomBinary(t *testing.T) {
	client := NewClaudeCLIClient(ClaudeCLIConfig{BinaryPath: "/usr/local/bin/claude-v2"})
	assert.Equal(t, "/usr/local/bin/claude-v2", client.binary)
}

func TestBuildPrompt_SingleMessage(t *testing.T) {
	result := buildPrompt([]Message{{Role: "user", Content: "Hello world"}})
	assert.Equal(t, "Hello world", result)
}

func TestBuildPrompt_MultiMessage(t *testing.T) {
	result := buildPrompt([]Message{
		{Role: "system", Content: "Be helpful"},
		{Role: "user", Content: "What is 2+2?"},
		{Role: "assistant", Content: "4"},
	})
	assert.Contains(t, result, "[System] Be helpful")
	assert.Contains(t, result, "[User] What is 2+2?")
	assert.Contains(t, result, "[Assistant] 4")
}
