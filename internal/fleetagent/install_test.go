// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstall_FreshInstallCreatesConfigAndKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.toml")
	keyPath := filepath.Join(dir, "host_ed25519")

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	res, err := Install(context.Background(), InstallOptions{
		ConfigPath:  cfgPath,
		RegistryURL: "http://127.0.0.1:7777",
		Logger:      logger,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.NodeID)
	assert.NotEmpty(t, res.HostKeyFingerprint)
	assert.Equal(t, keyPath, res.HostKeyPath)
	assert.False(t, res.HostKeyRegenerated)

	b, err := os.ReadFile(cfgPath) //nolint:gosec // G304 reads the test's own t.TempDir artifact
	require.NoError(t, err)
	assert.Contains(t, string(b), "node_id:")

	envBytes, err := os.ReadFile(filepath.Join(dir, "agent.env")) //nolint:gosec // G304 reads the test's own t.TempDir artifact
	require.NoError(t, err)
	assert.Contains(t, string(envBytes), "HELIXON_NODE_ID=")
}

func TestInstall_IdempotentReusesExistingKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.toml")

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	first, err := Install(context.Background(), InstallOptions{
		ConfigPath: cfgPath,
		Logger:     logger,
	})
	require.NoError(t, err)

	second, err := Install(context.Background(), InstallOptions{
		ConfigPath: cfgPath,
		Logger:     logger,
	})
	require.NoError(t, err)
	assert.Equal(t, first.HostKeyFingerprint, second.HostKeyFingerprint)
}

func TestEnsureHostKey_GeneratesValidPEM(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "host_ed25519")

	priv, pub, err := EnsureHostKey(keyPath, true)
	require.NoError(t, err)
	require.NotNil(t, priv)
	require.NotNil(t, pub)
	assert.Equal(t, ed25519.PrivateKeySize, len(priv))
	assert.Equal(t, ed25519.PublicKeySize, len(pub))

	b, err := os.ReadFile(keyPath) //nolint:gosec // G304 reads the test's own t.TempDir artifact
	require.NoError(t, err)
	block, _ := pem.Decode(b)
	require.NotNil(t, block)
	assert.Equal(t, PKCS8PrivateKeyPEMType, block.Type)
}

func TestEnsureHostKey_PreservesExistingKeyWithoutForce(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "host_ed25519")

	_, _, err := EnsureHostKey(keyPath, true)
	require.NoError(t, err)

	priv2, _, err := EnsureHostKey(keyPath, false)
	require.ErrorIs(t, err, ErrHostKeyExists)
	assert.NotNil(t, priv2)
}

func TestLoadConfig_NotFound(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "missing.toml"))
	assert.ErrorIs(t, err, ErrConfigNotFound)
}

func TestSaveConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	want := DefaultConfig("wsl-test", path)
	want.PeerAllowlist = []string{"wsl1", "wsl2"}

	require.NoError(t, SaveConfig(path, want))

	got, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, want.NodeID, got.NodeID)
	assert.Equal(t, want.PeerAllowlist, got.PeerAllowlist)
}
