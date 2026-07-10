package registra

import (
	"os"
	"path/filepath"
	"testing"
)

//nolint:unparam // body parameter is kept for future tests that write custom fixtures; current call sites use the default sampleYAML.
func writeTmpRegistry(t *testing.T, body string) string {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, "registry.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const sampleYAML = `schema_version: 1
services:
  - name: engramd
    kind: memory-server
    primary_node: node-a
    address: 127.0.0.1
    port: 8280
    health_path: /healthz
    binary: ~/local/bin/engramd
    owner_sprint: v14532
    source: sop/engram-install-node-a.md
    tags: [memory, node-a]
    status: registered
  - name: sprintboard-api
    kind: sprint-server
    primary_node: node-a
    address: 127.0.0.1
    port: 9400
    health_path: /healthz
    binary: ~/local/bin/sprintboard-api
    owner_sprint: v14533
    source: sop/sprintboard-install.md
    tags: [sprint, node-a]
    status: registered
  - name: llama-server-c7-q8
    kind: llm-cell
    primary_node: node-a
    address: 0.0.0.0
    port: 8010
    health_path: /health
    binary: ~/local/lib/llama/llama-server
    owner_sprint: v14530
    source: scripts/fleet/qwen36-matrix.yaml#C7
    tags: [llm, qwen, node-a]
    status: registered
nodes:
  - alias: node-a
    canonical_hostname: fleet-host-a-node-a
    tailscale_ip: 100.64.0.1
    role: central-node
llm_cells:
  - cell_id: C7
    node: node-a
    model_id: qwen36-27b-mtp-q8
    host_port: 8010
    status: ready
credentials_index:
  - id: abc123
    title: github pat
    vault: <vault-name>
    op_uri: op://<vault-name>/github pat
`

func TestRegistryLoadAndList(t *testing.T) {
	p := writeTmpRegistry(t, sampleYAML)
	r, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.SchemaVersion != 1 {
		t.Errorf("schema_version=%d want 1", r.SchemaVersion)
	}
	if got := len(r.Services); got != 3 {
		t.Errorf("services=%d want 3", got)
	}
	if got := len(r.Nodes); got != 1 {
		t.Errorf("nodes=%d want 1", got)
	}
	if got := len(r.LLMCells); got != 1 {
		t.Errorf("cells=%d want 1", got)
	}
	if got := len(r.CredentialsIndex); got != 1 {
		t.Errorf("credentials=%d want 1", got)
	}
}

func TestRegistryFindByName(t *testing.T) {
	p := writeTmpRegistry(t, sampleYAML)
	r, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s, ok := r.FindService("sprintboard-api")
	if !ok {
		t.Fatal("FindService(sprintboard-api) missing")
	}
	if s.Port != 9400 {
		t.Errorf("port=%d want 9400", s.Port)
	}
	if _, ok := r.FindService("does-not-exist"); ok {
		t.Error("missing service reported as found")
	}
}

func TestRegistryFilterByNode(t *testing.T) {
	p := writeTmpRegistry(t, sampleYAML)
	r, _ := Load(p)
	got := r.ServicesForNode("node-a")
	if len(got) != 3 {
		t.Errorf("node-a services=%d want 3", len(got))
	}
	got2 := r.ServicesForNode("node-d")
	if len(got2) != 0 {
		t.Errorf("node-d services=%d want 0", len(got2))
	}
}

func TestRegistryFilterByKind(t *testing.T) {
	p := writeTmpRegistry(t, sampleYAML)
	r, _ := Load(p)
	got := r.ServicesForKind("llm-cell")
	if len(got) != 1 {
		t.Errorf("llm-cell=%d want 1", len(got))
	}
	if got[0].Name != "llama-server-c7-q8" {
		t.Errorf("got=%s", got[0].Name)
	}
}

func TestRegistryFindCredentialByTitle(t *testing.T) {
	p := writeTmpRegistry(t, sampleYAML)
	r, _ := Load(p)
	c, ok := r.FindCredentialByTitle("github pat")
	if !ok {
		t.Fatal("missing cred")
	}
	if c.OPURI != "op://<vault-name>/github pat" {
		t.Errorf("op_uri=%s", c.OPURI)
	}
}

func TestRegistryFindNodeByAlias(t *testing.T) {
	p := writeTmpRegistry(t, sampleYAML)
	r, _ := Load(p)
	n, ok := r.FindNodeByAlias("node-a")
	if !ok {
		t.Fatal("missing node-a node")
	}
	if n.TailscaleIP != "100.64.0.1" {
		t.Errorf("tailscale_ip=%s", n.TailscaleIP)
	}
}
