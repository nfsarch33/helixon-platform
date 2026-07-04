package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

// AutoresearchConfig configures the autoresearch Helixon tool.
type AutoresearchConfig struct {
	// Binary is helix-dev-tools or the standalone autoresearch binary.
	Binary     string
	Iterations int
	Timeout    time.Duration
}

func (c AutoresearchConfig) withDefaults() AutoresearchConfig {
	if c.Binary == "" {
		if p := os.Getenv("HELIX_DEV_TOOLS"); p != "" {
			c.Binary = p
		} else {
			home, _ := os.UserHomeDir()
			c.Binary = filepath.Join(home, "bin", "helix-dev-tools")
		}
	}
	if c.Iterations <= 0 {
		c.Iterations = 5
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Minute
	}
	return c
}

// AutoresearchTool registers the overnight autoresearch loop as a Helixon tool.
func AutoresearchTool(cfg AutoresearchConfig) tooldispatch.ToolDef {
	cfg = cfg.withDefaults()
	return tooldispatch.ToolDef{
		Name:        "autoresearch_run",
		Description: "Run the 5-phase autoresearch probe→promote loop (emits agentrace NDJSON)",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"iterations": {"type": "integer", "description": "max iterations (default 5)"}
			}
		}`),
		Timeout: cfg.Timeout,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			iters := cfg.Iterations
			if v, ok := args["iterations"].(float64); ok && int(v) > 0 {
				iters = int(v)
			}
			runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(runCtx, cfg.Binary, "autoresearch", "run",
				"--iterations", fmt.Sprintf("%d", iters))
			cmd.Env = os.Environ()
			out, err := cmd.CombinedOutput()
			if err != nil {
				return truncateBytes(string(out), 64*1024), fmt.Errorf("autoresearch: %w", err)
			}
			return truncateBytes(string(out), 64*1024), nil
		},
	}
}
