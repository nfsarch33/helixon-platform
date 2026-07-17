package helixon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "helixon.yaml")
	body := `agent_id: helixon-claude
system_prompt: "You are a helpful agent."
session_dsn: "file:helixon.db?cache=shared&mode=rwc"
max_iterations: 17
max_tokens: 64000
timeout: "2m30s"
heartbeat_every: "45s"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // G306 test fixture
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AgentID != "helixon-claude" {
		t.Errorf("AgentID = %q", cfg.AgentID)
	}
	if cfg.MaxIterations != 17 {
		t.Errorf("MaxIterations = %d", cfg.MaxIterations)
	}
	if cfg.MaxTokens != 64000 {
		t.Errorf("MaxTokens = %d", cfg.MaxTokens)
	}
	if cfg.Timeout != 2*time.Minute+30*time.Second {
		t.Errorf("Timeout = %v", cfg.Timeout)
	}
	if cfg.HeartbeatEvery != 45*time.Second {
		t.Errorf("HeartbeatEvery = %v", cfg.HeartbeatEvery)
	}
}

func TestLoadConfig_EmptyFieldsLeaveZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "helixon.yaml")
	if err := os.WriteFile(path, []byte("agent_id: only\n"), 0o644); err != nil { //nolint:gosec // G306 test fixture
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Timeout != 0 || cfg.HeartbeatEvery != 0 {
		t.Errorf("expected zero durations, got %v / %v", cfg.Timeout, cfg.HeartbeatEvery)
	}
	// withDefaults should fill them in.
	cfg = cfg.withDefaults()
	if cfg.Timeout != 5*time.Minute {
		t.Errorf("default Timeout = %v", cfg.Timeout)
	}
	if cfg.HeartbeatEvery != 60*time.Second {
		t.Errorf("default HeartbeatEvery = %v", cfg.HeartbeatEvery)
	}
}

func TestLoadConfig_BadDurationReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("timeout: \"NOT A DURATION\"\n"), 0o644); err != nil { //nolint:gosec // G306 test fixture
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestLoadConfig_EmptyPathErrors(t *testing.T) {
	t.Parallel()
	if _, err := LoadConfig(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestLoadConfig_MissingFileErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := LoadConfig(filepath.Join(dir, "does-not-exist.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
