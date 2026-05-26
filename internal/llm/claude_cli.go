package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ClaudeCLIConfig holds configuration for the Claude Code CLI provider.
type ClaudeCLIConfig struct {
	BinaryPath string        // path to "claude" binary; defaults to "claude"
	Model      string        // model override (e.g. "claude-sonnet-4-20250514")
	MaxTokens  int           // max output tokens; 0 = CLI default
	Timeout    time.Duration // per-invocation timeout; 0 = 5 minutes
}

// ClaudeCLIClient implements Provider by shelling out to the Claude Code CLI.
// It uses the `claude -p --output-format json` interface for non-interactive use.
type ClaudeCLIClient struct {
	binary    string
	model     string
	maxTokens int
	timeout   time.Duration
	execFunc  func(ctx context.Context, name string, args ...string) ([]byte, error)
}

type claudeCLIResponse struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Model   string `json:"model"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	CostUSD float64 `json:"cost_usd"`
}

// NewClaudeCLIClient creates a Claude Code CLI provider.
func NewClaudeCLIClient(cfg ClaudeCLIConfig) *ClaudeCLIClient {
	binary := cfg.BinaryPath
	if binary == "" {
		binary = "claude"
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	return &ClaudeCLIClient{
		binary:    binary,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		timeout:   timeout,
		execFunc:  defaultExec,
	}
}

func defaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Complete sends the conversation to Claude Code CLI and parses the response.
func (c *ClaudeCLIClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	prompt := buildPrompt(req.Messages)

	args := []string{"-p", prompt, "--output-format", "json"}

	model := c.model
	if req.Model != "" {
		model = req.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	maxTok := c.maxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTok = *req.MaxTokens
	}
	if maxTok > 0 {
		args = append(args, "--max-turns", "1")
	}

	output, err := c.execFunc(ctx, c.binary, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: claude cli timed out after %s", ErrLLMTimeout, c.timeout)
		}
		return nil, fmt.Errorf("%w: claude cli: %v", ErrLLMClient, err)
	}

	return parseClaudeCLIOutput(output)
}

func buildPrompt(msgs []Message) string {
	if len(msgs) == 1 {
		return msgs[0].Content
	}
	var sb strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "system":
			sb.WriteString("[System] ")
		case "assistant":
			sb.WriteString("[Assistant] ")
		default:
			sb.WriteString("[User] ")
		}
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func parseClaudeCLIOutput(data []byte) (*CompletionResponse, error) {
	var cliResp claudeCLIResponse
	if err := json.Unmarshal(data, &cliResp); err != nil {
		content := strings.TrimSpace(string(data))
		if content == "" {
			return nil, fmt.Errorf("%w: empty response from claude cli", ErrLLMClient)
		}
		return &CompletionResponse{
			Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: content}}},
		}, nil
	}

	if cliResp.IsError {
		return nil, fmt.Errorf("%w: claude cli error: %s", ErrLLMClient, cliResp.Result)
	}

	return &CompletionResponse{
		Choices: []Choice{
			{Index: 0, Message: Message{Role: "assistant", Content: cliResp.Result}},
		},
		Usage: Usage{
			PromptTokens:     cliResp.Usage.InputTokens,
			CompletionTokens: cliResp.Usage.OutputTokens,
			TotalTokens:      cliResp.Usage.InputTokens + cliResp.Usage.OutputTokens,
		},
	}, nil
}
