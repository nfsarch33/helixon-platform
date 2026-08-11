// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the agent.toml v2 schema. v2 introduces ed25519 host key paths
// and the peer allowlist (replaces the implicit allowlist sourced from
// svcregistryd in v0.1.0).
type Config struct {
	NodeID        string   `yaml:"node_id"`
	HostKeyPath   string   `yaml:"host_key_path"`
	RegistryURL   string   `yaml:"registry_url"`
	HTTPAddr      string   `yaml:"http_addr"`
	PeerAllowlist []string `yaml:"peer_allowlist"`
	HeartbeatEvery string  `yaml:"heartbeat_every,omitempty"`
}

// ErrConfigNotFound is returned by LoadConfig when the file is missing.
var ErrConfigNotFound = errors.New("agent config not found")

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, ErrConfigNotFound
		}
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

func SaveConfig(path string, c Config) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	tmp := path + ".tmp"
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// DefaultConfig returns the seeded defaults for a fresh install. The
// configPath is used to place the host key alongside the config so the
// install is self-contained (no /etc/helixon dependency).
func DefaultConfig(nodeID, configPath string) Config {
	return Config{
		NodeID:        nodeID,
		HostKeyPath:   filepath.Join(filepath.Dir(configPath), "host_ed25519"),
		RegistryURL:   DefaultRegistryURL,
		HTTPAddr:      DefaultHTTPAddr,
		PeerAllowlist: []string{},
	}
}
