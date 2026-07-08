// Package qwen36 loads the canonical helixon fleet fleet LLM matrix
// (cursor-global-kb/scripts/fleet/qwen36-matrix.yaml) and exposes typed
// Go accessors that downstream tiers (cmd/choose-llm and the v14511
// beforeSubmitPrompt hook) can consume.
//
// v14510 contract:
//
//   - The YAML schema is owned by cursor-global-kb; this package must
//     accept additions without breaking. New cells are appended as new
//     Cell.ID values; missing fields default to zero.
//   - Lookup(repoRoot) MUST resolve scripts/fleet/qwen36-matrix.yaml
//     using a small set of well-known repo layouts so that the same
//     binary works whether invoked from the kb root, the helixon
//     checkout, or the home dir (when helixon is used as a side-car).
//   - Ready() returns only status==ready cells; that is the contract
//     for the tier router (a non-ready cell is never picked, even on
//     a force flag — the operator must flip the cell to ready after
//     download/sha256 verify).
package qwen36

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the current matrix schema version that this package
// understands. Matrix files with a newer schema_version MUST be rejected
// with ErrUnsupportedSchema so that operators update the helixon
// binary before relying on newer fields.
const SchemaVersion = 1

// ErrUnsupportedSchema is returned when a matrix file declares a
// schema_version that this package does not know how to read.
var ErrUnsupportedSchema = errors.New("qwen36 matrix: unsupported schema_version")

// CellStatusReady is the only status that the tier router is allowed
// to pick at runtime. metadata_blocked / downloading / corrupt cells
// must be flipped to ready by a human after the download + sha256
// verify phase completes.
const CellStatusReady = "ready"

// Matrix mirrors the structure of
// cursor-global-kb/scripts/fleet/qwen36-matrix.yaml. Cell is a value
// type so the map is JSON-serialisable when the CLI emits its
// decision; pass pointers only in hot paths.
type Matrix struct {
	SchemaVersion int             `yaml:"schema_version"`
	Cells         map[string]Cell `yaml:"cells"`
}

// Cell is one row of the matrix. The yaml tags are the canonical
// field names; anything not present in the source file is left at
// its zero value. New optional fields (tensor_split, spec_type,
// spec_draft_n_max, use_mmap, use_mlock, n_gpu_layers, context_size,
// ollama_alias) are stored as-is so a missing field on disk reads as
// the empty string / zero.
type Cell struct {
	ID             string `yaml:"-"`
	Node           string `yaml:"node"`
	GPUClass       string `yaml:"gpu_class"`
	GPUSlot        string `yaml:"gpu_slot"`
	ModelID        string `yaml:"model_id"`
	Repo           string `yaml:"repo"`
	Revision       string `yaml:"revision"`
	File           string `yaml:"file"`
	ExpectedBytes  int64  `yaml:"expected_bytes"`
	ActualBytes    int64  `yaml:"actual_bytes"`
	ActualSHA256   string `yaml:"actual_sha256"`
	Status         string `yaml:"status"`
	Engine         string `yaml:"engine"`
	HostPort       int    `yaml:"host_port"`
	MaxModelLen    int    `yaml:"max_model_len"`
	MinFreeMib     int    `yaml:"min_free_mib"`
	LocalPath      string `yaml:"local_path"`
	TensorSplit    string `yaml:"tensor_split,omitempty"`
	NGPULayers     int    `yaml:"n_gpu_layers,omitempty"`
	ContextSize    int    `yaml:"context_size,omitempty"`
	SpecType       string `yaml:"spec_type,omitempty"`
	SpecDraftNMax  int    `yaml:"spec_draft_n_max,omitempty"`
	UseMmap        bool   `yaml:"use_mmap"`
	UseMlock       bool   `yaml:"use_mlock"`
	OllamaAlias    string `yaml:"ollama_alias,omitempty"`
}

// LoadFile reads and validates a qwen36-matrix.yaml file from disk.
// Errors are wrapped with context so the CLI can surface a
// remediation hint to the operator (see cmd/choose-llm).
func LoadFile(path string) (*Matrix, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read matrix %q: %w", path, err)
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("load matrix %q: %w", path, err)
	}
	return m, nil
}

// Parse decodes a qwen36-matrix.yaml document from raw bytes. The
// returned Matrix has each Cell.ID populated from the map key so
// downstream consumers do not need to thread two pieces of state.
func Parse(raw []byte) (*Matrix, error) {
	var m Matrix
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse matrix yaml: %w", err)
	}
	if m.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, m.SchemaVersion, SchemaVersion)
	}
	if len(m.Cells) == 0 {
		return nil, errors.New("no cells in matrix")
	}
	// Stamp the map key into Cell.ID so consumers don't have to carry
	// the key alongside the value. Pre-existing IDs are preserved if
	// yaml unmarshal ever yields them (it doesn't today).
	for id := range m.Cells {
		c := m.Cells[id]
		c.ID = id
		m.Cells[id] = c
	}
	return &m, nil
}

// Lookup resolves the qwen36-matrix.yaml file from a repo root.
// The repo root is allowed to be:
//
//  1. The cursor-global-kb root (matrix lives at scripts/fleet/qwen36-matrix.yaml).
//  2. The helixon-platform root (the matrix is referenced via ../../cursor-global-kb/scripts/fleet).
//  3. The wsl1 home directory (caller passes "/home/jaslian" and we walk two levels up).
//
// We try paths in order and return the first one that exists. We
// deliberately do NOT search the filesystem recursively — finding an
// unrelated qwen36-matrix.yaml in a clone would silently pick the
// wrong one.
func Lookup(repoRoot string) (*Matrix, error) {
	candidates := candidateMatrixPaths(repoRoot)
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return LoadFile(p)
		}
	}
	return nil, fmt.Errorf("qwen36 matrix not found under %q (tried %v)", repoRoot, candidates)
}

// candidateMatrixPaths returns the well-known locations for the
// matrix, in priority order. Exposed for tests.
func candidateMatrixPaths(repoRoot string) []string {
	cleaned := filepath.Clean(repoRoot)
	return []string{
		filepath.Join(cleaned, "scripts", "fleet", "qwen36-matrix.yaml"),
		filepath.Join(cleaned, "..", "cursor-global-kb", "scripts", "fleet", "qwen36-matrix.yaml"),
		filepath.Join(cleaned, "..", "..", "cursor-global-kb", "scripts", "fleet", "qwen36-matrix.yaml"),
	}
}

// Ready returns only the cells whose status is "ready". The tier
// router is required to pick from this slice; the CLI surfaces a
// diagnostic listing non-ready cells when no ready cell matches.
func (m *Matrix) Ready() []Cell {
	out := make([]Cell, 0, len(m.Cells))
	for _, c := range m.Cells {
		if c.Status == CellStatusReady {
			out = append(out, c)
		}
	}
	return out
}

// BaseURL returns the OpenAI-compatible base URL for this cell.
// When hostOverride is empty the loopback address is used. The
// `/v1` suffix is appended because llama.cpp, vllm, and ollama all
// expose the chat completions endpoint under that path with the
// same response shape; downstream HTTP code can therefore treat the
// return as opaque.
func (c Cell) BaseURL(hostOverride string) string {
	host := hostOverride
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/v1", host, c.HostPort)
}
