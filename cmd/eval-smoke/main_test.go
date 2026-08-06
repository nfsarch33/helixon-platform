// runx-public-repo-gate: allow-file fleet_host_alias,network_topology,personal_path_id
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/llm/qwen36"
	"github.com/nfsarch33/helixon-platform/internal/smoke"
)

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
    spec_type: draft-mtp
`

func writeMatrix(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(p, []byte(fixtureMatrix), 0o644)) //nolint:gosec // G306 test fixture
	return p
}

func resolvePromptsPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	candidates := []string{
		filepath.Join(cwd, "..", "..", "eval-harness", "prompts-10.json"),
		filepath.Join(cwd, "..", "eval-harness", "prompts-10.json"),
		"/home/jaslian/Code/helixon-platform/eval-harness/prompts-10.json",
		"/mnt/c/Users/jaslian.DESKTOP-12RO1AF/Code/helixon-platform/eval-harness/prompts-10.json",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Fatalf("prompts-10.json not found (tried %v)", candidates)
	return ""
}

func TestSmoke_MockModeWalksAllTiers(t *testing.T) {
	matrixPath := writeMatrix(t)
	promptsPath := resolvePromptsPath(t)

	prompts, err := smoke.LoadPromptsFile(promptsPath)
	require.NoError(t, err, "prompts-10.json must exist")
	require.Len(t, prompts, 10)

	m, err := qwen36.LoadFile(matrixPath)
	require.NoError(t, err)

	out := runRun(m, prompts)
	assert.Equal(t, 10, out.Scoreboard.Total)
	assert.GreaterOrEqual(t, out.Scoreboard.Percentage(), 0.0)
}

func TestSmoke_BlockedMatrixFailsCleanly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "qwen36-matrix.yaml")
	require.NoError(t, os.WriteFile(p, []byte( //nolint:gosec // G306 test fixture
		"schema_version: 1\ncells:\n  X: {node: wsl1, gpu_class: rtx2070, gpu_slot: gpu0, model_id: blocked, repo: r/r, revision: main, file: m, expected_bytes: 0, status: metadata_blocked, engine: llama.cpp, host_port: 8001, max_model_len: 8192, min_free_mib: 1536, local_path: /tmp/m}\n",
	), 0o644))

	m, err := qwen36.LoadFile(p)
	require.NoError(t, err)
	prompts, err := smoke.LoadPromptsFile(resolvePromptsPath(t))
	require.NoError(t, err)

	out := runRun(m, prompts)
	assert.Equal(t, 10, out.Scoreboard.Total)
	assert.Equal(t, 0, out.Scoreboard.Passed, "all prompts must fail under blocked matrix")
}

func TestSmokeCLI_RunEmitsJSON(t *testing.T) {
	matrixPath := writeMatrix(t)
	promptsPath := resolvePromptsPath(t)

	stdout, _, err := runSmoke([]string{
		"run",
		"--matrix", matrixPath,
		"--prompts", promptsPath,
	})
	require.NoError(t, err)

	var payload struct {
		SprintID   string           `json:"sprint_id"`
		Mock       bool             `json:"mock"`
		Scoreboard smoke.Scoreboard `json:"scoreboard"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.Equal(t, "v14510", payload.SprintID)
	assert.True(t, payload.Mock)
	assert.Equal(t, 10, payload.Scoreboard.Total)
}

func TestSmokeCLI_HelpMentionsFlags(t *testing.T) {
	stdout, _, err := runSmoke([]string{"run", "--help"})
	require.NoError(t, err)
	assert.Contains(t, stdout, "--matrix")
	assert.Contains(t, stdout, "--prompts")
	assert.Contains(t, stdout, "--mock")
}

func TestSmokeCLI_VersionPrints(t *testing.T) {
	stdout, _, err := runSmoke([]string{"version"})
	require.NoError(t, err)
	assert.Contains(t, stdout, "eval-smoke")
}

// runRun is the test wrapper around production runMock so tests
// can drive it without spawning a CLI. It returns the same shape
// the binary writes to disk.
func runRun(m *qwen36.Matrix, prompts []smoke.Prompt) runnerOutput {
	results := runMock(m, prompts)
	board := smoke.Aggregate(results)
	return runnerOutput{Scoreboard: board, Results: results}
}

type runnerOutput struct {
	Scoreboard smoke.Scoreboard `json:"scoreboard"`
	Results    []smoke.Result   `json:"results"`
}

// runSmoke is the cobra CLI runner for cmd/eval-smoke.
//
//nolint:unparam // second return (stderr) is retained for symmetry with other CLI runners; tests ignore it intentionally.
func runSmoke(args []string, env ...map[string]string) (string, string, error) {
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	if len(env) > 0 {
		for k, v := range env[0] {
			old := os.Getenv(k)
			os.Setenv(k, v)
			defer os.Setenv(k, old)
		}
	}
	if err := root.Execute(); err != nil {
		return "", errBuf.String(), err
	}
	return out.String(), errBuf.String(), nil
}
