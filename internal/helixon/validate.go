package helixon

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Validate checks a RuntimeConfig for required fields and consistency.
// It collects every problem and returns them as a single error so an operator
// can fix all violations in one pass instead of compile-edit-loop.
//
// Required: AgentID, SessionDSN, MaxIterations>0, MaxTokens>0, Timeout>0,
// HeartbeatEvery>0. HeartbeatEvery must not exceed Timeout.
func (c RuntimeConfig) Validate() error {
	var probs []string
	if strings.TrimSpace(c.AgentID) == "" {
		probs = append(probs, "agent_id is required")
	}
	if strings.TrimSpace(c.SessionDSN) == "" {
		probs = append(probs, "session_dsn is required")
	}
	if c.MaxIterations <= 0 {
		probs = append(probs, fmt.Sprintf("max_iterations must be > 0 (got %d)", c.MaxIterations))
	}
	if c.MaxTokens <= 0 {
		probs = append(probs, fmt.Sprintf("max_tokens must be > 0 (got %d)", c.MaxTokens))
	}
	if c.Timeout <= 0 {
		probs = append(probs, fmt.Sprintf("timeout must be > 0 (got %s)", c.Timeout))
	}
	if c.HeartbeatEvery <= 0 {
		probs = append(probs, fmt.Sprintf("heartbeat_every must be > 0 (got %s)", c.HeartbeatEvery))
	}
	if c.Timeout > 0 && c.HeartbeatEvery > 0 && c.HeartbeatEvery > c.Timeout {
		probs = append(probs, fmt.Sprintf("heartbeat_every (%s) must not exceed timeout (%s)", c.HeartbeatEvery, c.Timeout))
	}
	if len(probs) == 0 {
		return nil
	}
	return errors.New("helixon config invalid:\n  - " + strings.Join(probs, "\n  - "))
}

// LoadAndValidate is the operator-facing entry point: read a YAML file, parse
// duration strings via FileConfig, then run Validate. No defaults are applied
// — the validator's job is to catch typos and missing fields explicitly.
func LoadAndValidate(path string) (RuntimeConfig, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return RuntimeConfig{}, err
	}
	if err := cfg.Validate(); err != nil {
		return RuntimeConfig{}, err
	}
	return cfg, nil
}

// writeFileImpl is exposed only for tests in the same package; package-level
// test helpers are kept tiny so the test files stay readable.
func writeFileImpl(path string, body []byte) error {
	return os.WriteFile(path, body, 0o644)
}

// Compile-time assurance that time package import is used in this file.
var _ = time.Second
