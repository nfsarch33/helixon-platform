// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegister_OK(t *testing.T) {
	var hits int32
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		assert.Equal(t, "/v1/nodes/register", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.toml")
	res, err := Install(context.Background(), InstallOptions{
		ConfigPath: cfgPath,
		Logger:     nopLogger(),
	})
	require.NoError(t, err)

	err = Register(context.Background(), RegisterOptions{
		ConfigPath:  cfgPath,
		RegistryURL: srv.URL,
		HTTPClient:  &http.Client{},
		Logger:      nopLogger(),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
	assert.Equal(t, res.NodeID, got["node_id"])
	assert.NotEmpty(t, got["key_fp"])
}

func TestRegister_FailsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.toml")
	_, err := Install(context.Background(), InstallOptions{
		ConfigPath: cfgPath,
		Logger:     nopLogger(),
	})
	require.NoError(t, err)

	err = Register(context.Background(), RegisterOptions{
		ConfigPath:  cfgPath,
		RegistryURL: srv.URL,
		HTTPClient:  &http.Client{},
		Logger:      nopLogger(),
	})
	require.Error(t, err)
}
