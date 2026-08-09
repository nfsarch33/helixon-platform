// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// InstallOptions configures the one-shot installer.
type InstallOptions struct {
	ConfigPath  string
	RegistryURL string
	Force       bool
	Logger      *slog.Logger
}

// InstallResult is the JSON envelope emitted on stdout.
type InstallResult struct {
	NodeID             string `json:"node_id"`
	ConfigPath         string `json:"config_path"`
	HostKeyPath        string `json:"host_key_path"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	HostKeyRegenerated bool   `json:"host_key_regenerated"`
	RegistryURL        string `json:"registry_url"`
	HTTPAddr           string `json:"http_addr"`
	Version            string `json:"version"`
}

// Install is the idempotent one-shot installer.
func Install(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	if err := ctx.Err(); err != nil {
		return InstallResult{}, err
	}
	if opts.Logger == nil {
		return InstallResult{}, errors.New("logger required")
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = DefaultConfigPath
	}
	if opts.RegistryURL == "" {
		opts.RegistryURL = DefaultRegistryURL
	}

	cfg, err := LoadConfig(opts.ConfigPath)
	switch {
	case err == nil:
		opts.Logger.Info("install: existing config found", "path", opts.ConfigPath, "node_id", cfg.NodeID)
	case errors.Is(err, ErrConfigNotFound):
		cfg = DefaultConfig(NodeID(), opts.ConfigPath)
		opts.Logger.Info("install: fresh config seeded", "node_id", cfg.NodeID)
	default:
		return InstallResult{}, fmt.Errorf("load config: %w", err)
	}

	if cfg.HostKeyPath == "" {
		cfg.HostKeyPath = filepath.Join(filepath.Dir(opts.ConfigPath), "host_ed25519")
	}
	cfg.RegistryURL = opts.RegistryURL

	priv, _, err := EnsureHostKey(cfg.HostKeyPath, opts.Force)
	if err != nil && !errors.Is(err, ErrHostKeyExists) {
		return InstallResult{}, fmt.Errorf("ensure host key: %w", err)
	}
	regenerated := err == nil

	fp := PublicKeyFingerprint(priv.Public().(ed25519.PublicKey))

	if err := SaveConfig(opts.ConfigPath, cfg); err != nil {
		return InstallResult{}, fmt.Errorf("save config: %w", err)
	}
	if err := writeEnvFile(cfg); err != nil {
		return InstallResult{}, fmt.Errorf("write env file: %w", err)
	}

	res := InstallResult{
		NodeID:             cfg.NodeID,
		ConfigPath:         opts.ConfigPath,
		HostKeyPath:        cfg.HostKeyPath,
		HostKeyFingerprint: fp,
		HostKeyRegenerated: regenerated,
		RegistryURL:        cfg.RegistryURL,
		HTTPAddr:           cfg.HTTPAddr,
		Version:            Version,
	}
	opts.Logger.Info("install: complete",
		"node_id", cfg.NodeID,
		"config", opts.ConfigPath,
		"host_key", cfg.HostKeyPath,
		"fingerprint", fp,
		"regenerated", regenerated,
	)
	return res, nil
}

// EnvelopeJSON renders the result as a stable JSON envelope.
func EnvelopeJSON(r InstallResult) string {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal: %v"}`, err)
	}
	return string(b)
}

func writeEnvFile(cfg Config) error {
	envPath := filepath.Join(filepath.Dir(cfg.HostKeyPath), "agent.env")
	contents := fmt.Sprintf("HELIXON_NODE_ID=%s\nHELIXON_HOST_KEY=%s\nHELIXON_REGISTRY=%s\nHELIXON_HTTP_ADDR=%s\n",
		cfg.NodeID, cfg.HostKeyPath, cfg.RegistryURL, cfg.HTTPAddr,
	)
	tmp := envPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, envPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, envPath, err)
	}
	return nil
}
