// runx-public-repo-gate: allow-file fleet_host_alias,network_topology,personal_path_id
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestBridgeToRegistryAPI ensures a static registry.yaml is converted into
// POST requests against the runtime svcregistry API.
func TestBridgeToRegistryAPI(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "registry.yaml")
	body := `schema_version: 1
services:
  - name: engramd
    kind: memory-server
    primary_node: wsl1
    address: 127.0.0.1
    port: 8280
    binary: ~/local/bin/engramd
    owner_sprint: v14532
  - name: sprintboard-api
    kind: sprint-server
    primary_node: wsl1
    address: 127.0.0.1
    port: 9400
    binary: ~/local/bin/sprintboard-api
    owner_sprint: v14533
nodes:
  - alias: wsl1
    tailscale_ip: 100.84.108.92
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil { //nolint:gosec // G306 test fixture
		t.Fatal(err)
	}

	// Set up a fake svcregistry API server.
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/services" && r.Method == http.MethodPost {
			var m map[string]any
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				t.Errorf("decode: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			got = append(got, m)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer func() { srv.Close() }()

	if err := bridge(yamlPath, srv.URL, "test-bridge"); err != nil {
		t.Logf("bridge err: %v", err)
		t.Fatalf("bridge: %v", err)
	}
	t.Logf("registered %d services", len(got))
	if len(got) != 2 {
		t.Errorf("registered %d, want 2", len(got))
	}
	// Verify first entry is the engramd service
	if got[0]["name"] != "engramd" {
		t.Errorf("name=%v", got[0]["name"])
	}
	if got[0]["host"] != "127.0.0.1" {
		t.Errorf("host=%v", got[0]["host"])
	}
	if got[0]["port"] != float64(8280) {
		t.Errorf("port=%v", got[0]["port"])
	}
	if got[0]["protocol"] != "http" {
		t.Errorf("protocol=%v", got[0]["protocol"])
	}
	if got[0]["tailscale_ip"] != "100.84.108.92" {
		t.Errorf("tailscale_ip=%v", got[0]["tailscale_ip"])
	}
	if got[0]["owner"] != "test-bridge" {
		t.Errorf("owner=%v want test-bridge", got[0]["owner"])
	}
}
