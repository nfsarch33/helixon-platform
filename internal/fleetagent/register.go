// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// RegisterOptions configures the register subcommand.
type RegisterOptions struct {
	ConfigPath  string
	RegistryURL string
	Logger      *slog.Logger
	HTTPClient  *http.Client
}

// Register announces this node to svcregistryd.
func Register(ctx context.Context, opts RegisterOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.Logger == nil {
		return errors.New("logger required")
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = DefaultConfigPath
	}
	if opts.RegistryURL == "" {
		opts.RegistryURL = DefaultRegistryURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: HeartbeatTimeout}
	}

	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config %s: %w", opts.ConfigPath, err)
	}

	priv, _, err := EnsureHostKey(cfg.HostKeyPath, false)
	if err != nil && !errors.Is(err, ErrHostKeyExists) {
		return fmt.Errorf("ensure host key: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	fp := PublicKeyFingerprint(pub)

	payload := map[string]any{
		"node_id":   cfg.NodeID,
		"version":   Version,
		"http_addr": cfg.HTTPAddr,
		"key_fp":    fp,
		"ts":        time.Now().UTC().Format(time.RFC3339Nano),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.RegistryURL+"/v1/nodes/register", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("register status %d", resp.StatusCode)
	}
	opts.Logger.Info("register: ok", "node_id", cfg.NodeID, "url", opts.RegistryURL)
	return nil
}
