// Package registra is the Helixon Service Registry loader and query API.
// It loads the canonical registry (registry.yaml/registry.json) and exposes
// the read-only data structures used by helix-dev-tools, doctor suites,
// Evospine, agent-browser, and the agent fleet.
package registra

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Service is one entry in the registry.
type Service struct {
	Name        string   `yaml:"name" json:"name"`
	Kind        string   `yaml:"kind" json:"kind"`
	PrimaryNode string   `yaml:"primary_node" json:"primary_node"`
	Address     string   `yaml:"address" json:"address"`
	Port        int      `yaml:"port" json:"port"`
	HealthPath  string   `yaml:"health_path,omitempty" json:"health_path,omitempty"`
	Binary      string   `yaml:"binary" json:"binary"`
	OwnerSprint string   `yaml:"owner_sprint" json:"owner_sprint"`
	Source      string   `yaml:"source" json:"source"`
	Tags        []string `yaml:"tags" json:"tags"`
	Status      string   `yaml:"status" json:"status"`
}

// Node is one Helixon fleet machine (wsl1, wsl2, etc).
type Node struct {
	Alias             string   `yaml:"alias" json:"alias"`
	CanonicalHostname string   `yaml:"canonical_hostname" json:"canonical_hostname"`
	TailscaleIP       string   `yaml:"tailscale_ip" json:"tailscale_ip"`
	OS                string   `yaml:"os" json:"os"`
	User              string   `yaml:"user" json:"user"`
	SSHPort           int      `yaml:"ssh_port" json:"ssh_port"`
	Role              string   `yaml:"role" json:"role"`
	GPUs              []string `yaml:"gpus" json:"gpus,omitempty"`
	Notes             string   `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// LLMCell is a single model cell entry from qwen36-matrix.yaml.
type LLMCell struct {
	CellID    string `yaml:"cell_id" json:"cell_id"`
	Node      string `yaml:"node" json:"node"`
	GPUClass  string `yaml:"gpu_class" json:"gpu_class"`
	GPUSlot   string `yaml:"gpu_slot" json:"gpu_slot"`
	ModelID   string `yaml:"model_id" json:"model_id"`
	Engine    string `yaml:"engine" json:"engine"`
	HostPort  int    `yaml:"host_port" json:"host_port"`
	Status    string `yaml:"status" json:"status"`
	OpenAIURL string `yaml:"openai_compat_url" json:"openai_compat_url"`
}

// Credential is a 1Password vault item index entry.
type Credential struct {
	ID       string `yaml:"id" json:"id"`
	Title    string `yaml:"title" json:"title"`
	Category string `yaml:"category" json:"category"`
	Vault    string `yaml:"vault" json:"vault"`
	OPURI    string `yaml:"op_uri" json:"op_uri"`
}

// Registry is the top-level document.
type Registry struct {
	SchemaVersion    int          `yaml:"schema_version" json:"schema_version"`
	RegistryVersion  string       `yaml:"registry_version" json:"registry_version"`
	RegistryID       string       `yaml:"registry_id" json:"registry_id"`
	Fleet            string       `yaml:"fleet" json:"fleet"`
	Operator         string       `yaml:"operator" json:"operator"`
	CentralNode      string       `yaml:"central_node" json:"central_node"`
	Updated          string       `yaml:"updated" json:"updated"`
	SourceFiles      []string     `yaml:"source_files" json:"source_files"`
	Services         []Service    `yaml:"services" json:"services"`
	Nodes            []Node       `yaml:"nodes" json:"nodes"`
	LLMCells         []LLMCell    `yaml:"llm_cells" json:"llm_cells"`
	CredentialsIndex []Credential `yaml:"credentials_index" json:"credentials_index"`
}

// Load reads registry.yaml from disk and parses it.
func Load(path string) (*Registry, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var r Registry
	if err := yaml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &r, nil
}

// FindService returns the service entry for the given name, or false.
func (r *Registry) FindService(name string) (Service, bool) {
	for _, s := range r.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// ServicesForNode returns all services whose primary_node matches.
func (r *Registry) ServicesForNode(node string) []Service {
	var out []Service
	for _, s := range r.Services {
		if s.PrimaryNode == node {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// ServicesForKind returns all services whose kind matches.
func (r *Registry) ServicesForKind(kind string) []Service {
	var out []Service
	for _, s := range r.Services {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// FindCredentialByTitle returns the first credential entry whose title matches.
func (r *Registry) FindCredentialByTitle(title string) (Credential, bool) {
	for _, c := range r.CredentialsIndex {
		if c.Title == title {
			return c, true
		}
	}
	return Credential{}, false
}

// FindNodeByAlias returns the node matching the alias.
func (r *Registry) FindNodeByAlias(alias string) (Node, bool) {
	for _, n := range r.Nodes {
		if n.Alias == alias {
			return n, true
		}
	}
	return Node{}, false
}
