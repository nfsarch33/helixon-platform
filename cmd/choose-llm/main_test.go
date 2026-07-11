package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureMatrix is a small valid matrix used by CLI tests.
const fixtureMatrix = `schema_version: 1
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
    spec_type: draft-mtp
  C99:
    node: wsl1
    gpu_class: rtx2070
    gpu_slot: gpu0
    model_id: qwen36-4b-q4-blocked
    repo: unsloth/Qwen3.6-4B-GGUF
    revision: main
    file: Qwen3.6-4B-Q4_K_M.gguf
    expected_bytes: 0
    status: metadata_blocked
    engine: llama.cpp
    host_port: 8008
    max_model_len: 8192
    min_free_mib: 1536
    local_path: /mnt/f/models/qwen36-gguf/missing.gguf
`

func writeFixtureMatrix(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(path, []byte(fixtureMatrix), 0o644))
	return path
}

// runWith is the canonical "run the cobra root cmd with the supplied
// args, capture stdout + stderr" helper. Cobra's writer fan-out is
// funnelled via SetOut / SetErr; child commands that use
// cmd.OutOrStdout() inherit the root's writer so this is sufficient
// (no os.Stdout swap needed, so the test is race-safe with -race).
//
//nolint:unparam // env parameter is kept for future env-var tests; signature stability outweighs the linter flag.
func runWith(args []string, env map[string]string) (stdout string, stderr string, err error) {
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	for k, v := range env {
		t := os.Getenv(k)
		os.Setenv(k, v)
		defer os.Setenv(k, t)
	}
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestPick_FlagsRecognisedAndHelp(t *testing.T) {
	// The root --help only lists subcommands; matrix and tier flags
	// are defined on `pick`, so we ask pick for its help text.
	stdout, _, err := runWith([]string{"pick", "--help"}, nil)
	require.NoError(t, err)
	assert.Contains(t, stdout, "choose-llm")
	assert.Contains(t, stdout, "--matrix")
	assert.Contains(t, stdout, "--tier")
}

func TestPick_ResolvesMatrixEmitsJSON(t *testing.T) {
	path := writeFixtureMatrix(t)
	stdout, stderr, err := runWith([]string{
		"pick",
		"--matrix", path,
		"--tier", "3",
		"--host-override", "wsl1.tail447712.ts.net",
	}, nil)
	require.NoError(t, err, "stderr=%s", stderr)

	var got pickOutput
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))
	assert.Equal(t, "C7", got.CellID)
	// BaseURLStripped keeps the /v1 suffix so callers can paste it
	// directly into a curl invocation.
	assert.Equal(t, "wsl1.tail447712.ts.net:8010/v1", got.BaseURLStripped)
	assert.Equal(t, "qwen36-27b-mtp-q8", got.ModelID)
	assert.Equal(t, "draft-mtp", got.SpecType)
	assert.Equal(t, 3, got.Tier)
	assert.Equal(t, "tier3 prefers speculative-decoding-capable cells", got.Reason)
}

func TestPick_Tier0ChoosesSmallest(t *testing.T) {
	path := writeFixtureMatrix(t)
	stdout, _, err := runWith([]string{
		"pick",
		"--matrix", path,
		"--tier", "0",
	}, nil)
	require.NoError(t, err)
	var got pickOutput
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))
	// C1 and C7 are ready; tier0 wants small which means highest
	// min_free_mib in the "cheap" bucket, but with the current
	// scoreTier0 the lower min_free_mib wins. Both ready cells
	// are large-context; pick should be C7 because max_model_len
	// for C7 is 32768 vs C1 65536 — no, the score formula
	// 2_000_000 - min_free_mib favours lower min_free_mib. So
	// C1 (4096) wins over C7 (49152). Both cells are explicit.
	assert.Equal(t, "C1", got.CellID)
}

func TestPick_TierFlagRejectedOutOfRange(t *testing.T) {
	path := writeFixtureMatrix(t)
	_, _, err := runWith([]string{"pick", "--matrix", path, "--tier", "9"}, nil)
	require.Error(t, err)
}

func TestPick_TierFlagNegativeRejected(t *testing.T) {
	path := writeFixtureMatrix(t)
	_, _, err := runWith([]string{"pick", "--matrix", path, "--tier", "-1"}, nil)
	require.Error(t, err)
}

func TestPick_MissingMatrixFileFails(t *testing.T) {
	_, _, err := runWith([]string{"pick", "--matrix", "/no/such/file.yaml", "--tier", "0"}, nil)
	require.Error(t, err)
}

func TestPick_AllBlockedFailsWithNoReadyCell(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"schema_version: 1\ncells:\n  X: {node: wsl1, gpu_class: rtx2070, gpu_slot: gpu0, model_id: blocked, repo: r/r, revision: main, file: m, expected_bytes: 0, status: metadata_blocked, engine: llama.cpp, host_port: 8001, max_model_len: 8192, min_free_mib: 1536, local_path: /tmp/m}\n",
	), 0o644))

	_, stderr, err := runWith([]string{"pick", "--matrix", path, "--tier", "0"}, nil)
	require.Error(t, err)
	assert.Contains(t, stderr, "no ready")
}

func TestMatrixList_RendersReadyAndBlocked(t *testing.T) {
	path := writeFixtureMatrix(t)
	stdout, _, err := runWith([]string{"matrix", "list", "--matrix", path}, nil)
	require.NoError(t, err)
	assert.Contains(t, stdout, "C1")
	assert.Contains(t, stdout, "C7")
	assert.Contains(t, stdout, "C99")
	assert.Contains(t, stdout, "ready")
	assert.Contains(t, stdout, "metadata_blocked")
}

func TestVersion_Prints(t *testing.T) {
	stdout, _, err := runWith([]string{"version"}, nil)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, "choose-llm "))
}

func init() {
	// test scaffolding; no runtime pin needed
}
