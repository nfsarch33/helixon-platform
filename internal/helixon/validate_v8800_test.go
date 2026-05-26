// T-8800-B12: helixon config validator. RuntimeConfig.Validate() reports
// all field-level violations as a single multi-error so operators see every
// missing/invalid field in one shot, not one-at-a-time.
package helixon

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeConfig_Validate_HappyPath(t *testing.T) {
	t.Parallel()
	cfg := RuntimeConfig{
		AgentID:        "ok",
		SystemPrompt:   "you are helpful",
		SessionDSN:     "file::memory:?cache=shared",
		MaxIterations:  5,
		MaxTokens:      4096,
		Timeout:        30 * time.Second,
		HeartbeatEvery: 10 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRuntimeConfig_Validate_ReportsAllMissing(t *testing.T) {
	t.Parallel()
	// Empty everything: every required field should be flagged once.
	var cfg RuntimeConfig
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"agent_id", "session_dsn", "max_iterations", "max_tokens", "timeout", "heartbeat_every"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in: %s", want, msg)
		}
	}
}

func TestRuntimeConfig_Validate_RejectsNegativeNumeric(t *testing.T) {
	t.Parallel()
	cfg := RuntimeConfig{
		AgentID:        "x",
		SessionDSN:     "file::memory:",
		MaxIterations:  -1,
		MaxTokens:      -1,
		Timeout:        -time.Second,
		HeartbeatEvery: -time.Second,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"max_iterations", "max_tokens", "timeout", "heartbeat_every"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in: %s", want, err.Error())
		}
	}
}

func TestRuntimeConfig_Validate_HeartbeatGreaterThanTimeoutFlagged(t *testing.T) {
	t.Parallel()
	cfg := RuntimeConfig{
		AgentID:        "x",
		SessionDSN:     "file::memory:",
		MaxIterations:  5,
		MaxTokens:      4096,
		Timeout:        2 * time.Second,
		HeartbeatEvery: 10 * time.Second,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for heartbeat > timeout")
	}
	if !strings.Contains(err.Error(), "heartbeat_every") {
		t.Fatalf("expected heartbeat_every in: %s", err.Error())
	}
}

func TestLoadAndValidate_ReturnsValidationError(t *testing.T) {
	t.Parallel()
	// Empty session_dsn (yaml leaves it as the zero string), so validation
	// should fire even though Loader didn't.
	dir := t.TempDir()
	path := dir + "/cfg.yaml"
	if err := writeFile(path, "agent_id: only\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadAndValidate(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadAndValidate_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/cfg.yaml"
	body := `agent_id: helixon
system_prompt: ok
session_dsn: "file::memory:"
max_iterations: 5
max_tokens: 4096
timeout: "30s"
heartbeat_every: "10s"
`
	if err := writeFile(path, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadAndValidate(path)
	if err != nil {
		t.Fatalf("LoadAndValidate: %v", err)
	}
	if cfg.AgentID != "helixon" {
		t.Fatalf("AgentID = %q", cfg.AgentID)
	}
}

// writeFile is a tiny helper to keep the tests linear.
func writeFile(path, body string) error {
	return writeFileImpl(path, []byte(body))
}
