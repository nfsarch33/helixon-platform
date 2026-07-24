// runx-public-repo-gate: allow-file fleet_host_alias
package qwen36

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleMatrix = `# v14510 sample matrix used by qwen36 matrix tests.
# Schema mirrors cursor-global-kb/scripts/fleet/qwen36-matrix.yaml:1
schema_version: 1
cells:
  C1:
    node: wsl1
    gpu_class: rtx3090
    gpu_slot: gpu1
    model_id: qwen36-27b-int4
    repo: cyankiwi/Qwen3.6-27B-AWQ-INT4
    revision: 42cf87020d298736061e41fd2673bc631f1cc4c6
    file: ''
    expected_bytes: 20467235944
    status: ready
    engine: vllm
    host_port: 8004
    max_model_len: 65536
    min_free_mib: 4096
    local_path: /mnt/f/models/Qwen3.6-27B-AWQ-INT4
  C2:
    node: wsl1
    gpu_class: rtx3090
    gpu_slot: gpu2
    model_id: qwen36-27b-q4
    repo: unsloth/Qwen3.6-27B-GGUF
    revision: 82d411acf4a06cfb8d9b073a5211bf410bfc29bf
    file: Qwen3.6-27B-Q4_K_M.gguf
    expected_bytes: 16817244384
    status: ready
    engine: llama.cpp
    host_port: 8005
    max_model_len: 65536
    min_free_mib: 4096
    local_path: /mnt/g/models/qwen36-gguf/Qwen3.6-27B-Q4_K_M.gguf
  C7:
    node: wsl1
    gpu_class: rtx3090-dual
    gpu_slot: gpu0+gpu1
    model_id: qwen36-27b-mtp-q8
    repo: unsloth/Qwen3.6-27B-MTP-GGUF
    revision: main
    file: Qwen3.6-27B-Q8_0.gguf
    expected_bytes: 31106361344
    status: ready
    engine: llama.cpp
    host_port: 8010
    max_model_len: 32768
    min_free_mib: 49152
    local_path: /mnt/g/models/Qwen3.6-27B-MTP-GGUF/Qwen3.6-27B-Q8_0.gguf
    tensor_split: 24,24
    n_gpu_layers: 99
    context_size: 32768
    spec_type: draft-mtp
    spec_draft_n_max: 2
    use_mmap: true
    use_mlock: false
  C14:
    node: wsl1
    gpu_class: rtx3090
    gpu_slot: gpu2
    model_id: qwen36-27b-q4-not-yet
    repo: unsloth/Qwen3.6-27B-GGUF
    revision: 82d411acf4a06cfb8d9b073a5211bf410bfc29bf
    file: Qwen3.6-27B-Q4_K_M.gguf
    expected_bytes: 16817244384
    status: metadata_blocked
    engine: llama.cpp
    host_port: 8011
    max_model_len: 32768
    min_free_mib: 4096
    local_path: /mnt/g/models/qwen36-gguf/does-not-exist.gguf
`

func writeMatrixFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(p, []byte(sampleMatrix), 0o644)) //nolint:gosec // G306 test fixture
	return p
}

func TestLoad_ReadsCanonicalMatrix(t *testing.T) {
	dir := t.TempDir()
	p := writeMatrixFile(t, dir)

	m, err := LoadFile(p)
	require.NoError(t, err)
	require.NotNil(t, m)

	assert.Equal(t, 1, m.SchemaVersion)
	assert.Len(t, m.Cells, 4, "expected C1, C2, C7, C14")

	c2, ok := m.Cells["C2"]
	require.True(t, ok)
	assert.Equal(t, "wsl1", c2.Node)
	assert.Equal(t, "rtx3090", c2.GPUClass)
	assert.Equal(t, "gpu2", c2.GPUSlot)
	assert.Equal(t, "qwen36-27b-q4", c2.ModelID)
	assert.Equal(t, "llama.cpp", c2.Engine)
	assert.Equal(t, 8005, c2.HostPort)
	assert.Equal(t, int64(16817244384), c2.ExpectedBytes)
	assert.Equal(t, "ready", c2.Status)
	assert.Equal(t, "Qwen3.6-27B-Q4_K_M.gguf", c2.File)
	assert.Empty(t, c2.TensorSplit, "C2 has no tensor_split")
}

func TestLoad_PreservesTensorSplitString(t *testing.T) {
	dir := t.TempDir()
	p := writeMatrixFile(t, dir)
	m, err := LoadFile(p)
	require.NoError(t, err)

	c7, ok := m.Cells["C7"]
	require.True(t, ok)
	assert.Equal(t, "24,24", c7.TensorSplit)
	assert.Equal(t, "draft-mtp", c7.SpecType)
	assert.Equal(t, 2, c7.SpecDraftNMax)
}

func TestLoad_RejectsMissingFile(t *testing.T) {
	_, err := LoadFile("/no/such/file.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read matrix")
}

func TestLoad_RejectsMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(bad, []byte(":\n  :\n  - [[\n"), 0o644)) //nolint:gosec // G306 test fixture

	_, err := LoadFile(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse matrix")
}

func TestLoad_RejectsEmptyCells(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(empty, []byte("schema_version: 1\ncells: {}\n"), 0o644)) //nolint:gosec // G306 test fixture

	_, err := LoadFile(empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cells")
}

func TestLookup_ResolvesRelativeRepoRoot(t *testing.T) {
	dir := t.TempDir()
	cursorKBSubdir := filepath.Join(dir, "subdir", "nested")
	require.NoError(t, os.MkdirAll(cursorKBSubdir, 0o755)) //nolint:gosec // G301 test fixture

	kbbasedir := filepath.Dir(filepath.Dir(cursorKBSubdir))
	require.NoError(t, os.MkdirAll(filepath.Join(kbbasedir, "scripts", "fleet"), 0o755)) //nolint:gosec // G301 test fixture

	matrixPath := filepath.Join(kbbasedir, "scripts", "fleet", "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(matrixPath, []byte(sampleMatrix), 0o644)) //nolint:gosec // G306 test fixture

	m, err := Lookup(kbbasedir)
	require.NoError(t, err)
	assert.Len(t, m.Cells, 4)
}

func TestReady_FiltersNonReadyCells(t *testing.T) {
	dir := t.TempDir()
	p := writeMatrixFile(t, dir)
	m, err := LoadFile(p)
	require.NoError(t, err)

	ready := m.Ready()
	names := make([]string, 0, len(ready))
	for _, c := range ready {
		names = append(names, c.ID)
	}
	assert.ElementsMatch(t, []string{"C1", "C2", "C7"}, names, "only ready cells should be returned; C14 metadata_blocked is excluded")
}

func TestBaseURL_BuildsOpenAICompatFromHostPort(t *testing.T) {
	c := Cell{
		Node:     "wsl1",
		HostPort: 8005,
		Engine:   "llama.cpp",
	}
	assert.Equal(t, "http://127.0.0.1:8005/v1", c.BaseURL(""))

	got := c.BaseURL("wsl1.tail447712.ts.net")
	assert.Equal(t, "http://wsl1.tail447712.ts.net:8005/v1", got)
}
