package helixon

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// FileConfig is the YAML on-disk shape for a helixon Runtime. It is
// deliberately a flat mirror of RuntimeConfig with string durations so the
// file is human-editable.
//
// Example:
//
//	agent_id: "helixon-claude"
//	system_prompt: "You are a helpful agent."
//	session_dsn: "file:helixon.db?cache=shared&mode=rwc"
//	max_iterations: 25
//	max_tokens: 128000
//	timeout: "5m"
//	heartbeat_every: "60s"
type FileConfig struct {
	AgentID        string                `yaml:"agent_id"`
	SystemPrompt   string                `yaml:"system_prompt"`
	SessionDSN     string                `yaml:"session_dsn"`
	MaxIterations  int                   `yaml:"max_iterations"`
	MaxTokens      int                   `yaml:"max_tokens"`
	Timeout        string                `yaml:"timeout"`
	HeartbeatEvery string                `yaml:"heartbeat_every"`
	Provider       ProviderConfig        `yaml:"provider"`
	Sprintboard    SprintboardFileConfig `yaml:"sprintboard"`
}

// SprintboardFileConfig is the YAML shape for sprintboard integration.
type SprintboardFileConfig struct {
	URL          string `yaml:"url"`
	Capabilities string `yaml:"capabilities"`
}

// LoadConfig reads a YAML file at path and returns the parsed RuntimeConfig.
// Empty fields keep their RuntimeConfig zero-value so withDefaults applies.
func LoadConfig(path string) (RuntimeConfig, error) {
	if path == "" {
		return RuntimeConfig{}, errors.New("helixon: empty config path")
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("helixon: read %s: %w", path, err)
	}
	var fc FileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return RuntimeConfig{}, fmt.Errorf("helixon: parse %s: %w", path, err)
	}
	return fc.ToRuntimeConfig()
}

// ToRuntimeConfig converts a FileConfig into a RuntimeConfig, parsing the
// duration strings. An empty duration string maps to zero so withDefaults
// can apply the runtime default.
func (fc FileConfig) ToRuntimeConfig() (RuntimeConfig, error) {
	cfg := RuntimeConfig{
		AgentID:       fc.AgentID,
		SystemPrompt:  fc.SystemPrompt,
		SessionDSN:    fc.SessionDSN,
		MaxIterations: fc.MaxIterations,
		MaxTokens:     fc.MaxTokens,
	}
	if fc.Timeout != "" {
		d, err := time.ParseDuration(fc.Timeout)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("helixon: parse timeout %q: %w", fc.Timeout, err)
		}
		cfg.Timeout = d
	}
	if fc.HeartbeatEvery != "" {
		d, err := time.ParseDuration(fc.HeartbeatEvery)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("helixon: parse heartbeat_every %q: %w", fc.HeartbeatEvery, err)
		}
		cfg.HeartbeatEvery = d
	}
	cfg.Provider = fc.Provider
	cfg.SprintboardURL = fc.Sprintboard.URL
	cfg.SprintboardCapabilities = fc.Sprintboard.Capabilities
	if fc.Provider.TimeoutString != "" {
		d, err := time.ParseDuration(fc.Provider.TimeoutString)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("helixon: parse provider.timeout %q: %w", fc.Provider.TimeoutString, err)
		}
		cfg.Provider.Timeout = d
	}
	return cfg, nil
}
