package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
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
	defer srv.Close()

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

func TestBridge_BadYAMLPath(t *testing.T) {
	err := bridge("/nonexistent/registry.yaml", "http://127.0.0.1:9999", "owner")
	if err == nil {
		t.Error("expected error for nonexistent yaml path")
	}
}

func TestBridge_StatusError(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "registry.yaml")
	body := `schema_version: 1
services:
  - name: svc1
    kind: x
    primary_node: wsl1
    address: 127.0.0.1
    port: 8001
    binary: /bin/true
    owner_sprint: v14532
nodes:
  - alias: wsl1
    tailscale_ip: 100.0.0.1
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"db down"}`))
	}))
	defer srv.Close()

	err := bridge(yamlPath, srv.URL, "owner")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestBridge_SkipsZeroPort(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "registry.yaml")
	body := `schema_version: 1
services:
  - name: noservice
    kind: x
    primary_node: wsl1
    address: 127.0.0.1
    port: 0
    binary: /bin/true
    owner_sprint: v14532
  - name: realservice
    kind: x
    primary_node: wsl1
    address: 127.0.0.1
    port: 9100
    binary: /bin/true
    owner_sprint: v14532
nodes:
  - alias: wsl1
    tailscale_ip: 100.0.0.1
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := bridge(yamlPath, srv.URL, "owner"); err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 POST (port=0 skipped), got %d", count)
	}
}

func TestPostJSON_InvalidJSON(t *testing.T) {
	// Channel can't be marshaled — verify error path
	err := postJSON("http://127.0.0.1:1/x", make(chan int), time.Second)
	if err == nil {
		t.Error("expected error from unmarshalable body")
	}
}

func TestPostJSON_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	err := postJSON(srv.URL+"/missing", map[string]any{"x": 1}, time.Second)
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestPostJSON_InvalidURL(t *testing.T) {
	err := postJSON("http://[::1]:badport/x", map[string]any{"x": 1}, time.Second)
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestPostJSON_ConnectionRefused(t *testing.T) {
	err := postJSON("http://127.0.0.1:1/nothing", map[string]any{"x": 1}, time.Second)
	if err == nil {
		t.Error("expected error for refused connection")
	}
}

func TestRunAll_BadYAMLPath(t *testing.T) {
	ok, fail, _ := runAll(bridgeOptions{
		RegistryPath: "/nonexistent/registry.yaml",
		APIURL:       "http://127.0.0.1:1",
		Owner:        "test",
		Timeout:      time.Second,
	})
	if fail != 1 {
		t.Errorf("expected fail=1 for bad yaml, got ok=%d fail=%d", ok, fail)
	}
}

func TestRunAll_DryRun(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "registry.yaml")
	body := `schema_version: 1
services:
  - name: svc-dry
    kind: x
    primary_node: wsl1
    address: 127.0.0.1
    port: 8888
    binary: /bin/true
    owner_sprint: v14532
  - name: svc-noport
    kind: x
    primary_node: wsl1
    address: 127.0.0.1
    port: 0
    binary: /bin/true
    owner_sprint: v14532
nodes:
  - alias: wsl1
    tailscale_ip: 100.0.0.1
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, fail, skip := runAll(bridgeOptions{
		RegistryPath: yamlPath,
		APIURL:       "http://127.0.0.1:1",
		Owner:        "test",
		DryRun:       true,
		Timeout:      time.Second,
	})
	if ok != 1 {
		t.Errorf("expected ok=1, got %d", ok)
	}
	if fail != 0 {
		t.Errorf("expected fail=0 in dry-run, got %d", fail)
	}
	if skip != 1 {
		t.Errorf("expected skip=1 for port=0, got %d", skip)
	}
}

func TestRunAll_AllFail(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "registry.yaml")
	body := `schema_version: 1
services:
  - name: svc-bad
    kind: x
    primary_node: wsl1
    address: 127.0.0.1
    port: 9999
    binary: /bin/true
    owner_sprint: v14532
nodes:
  - alias: wsl1
    tailscale_ip: 100.0.0.1
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, fail, _ := runAll(bridgeOptions{
		RegistryPath: yamlPath,
		APIURL:       "http://127.0.0.1:1",
		Owner:        "test",
		Timeout:      time.Second,
	})
	if fail != 1 || ok != 0 {
		t.Errorf("expected fail=1 ok=0, got ok=%d fail=%d", ok, fail)
	}
}
