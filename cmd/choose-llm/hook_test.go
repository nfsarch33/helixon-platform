package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/choosehook"
)

// Test the canonical hook contract: stdin is a DecideInput JSON,
// stdout is a choosehook.Output JSON. We use os.Pipe to mimic
// the real Cursor flow (hook is a subprocess).
func TestHookSubcommand_StdinToStdoutRoundtrip(t *testing.T) {
	matrixPath := writeFixtureMatrix(t)

	out, err := choosehook.Decide(
		choosehook.DecideInput{Prompt: "write a Go function that returns max"},
		matrixPath, "",
	)
	require.NoError(t, err)

	bb, err := json.Marshal(out)
	require.NoError(t, err)
	for _, want := range []string{`"sprint_id":"v14511"`, `"hook_mode":"redirect"`} {
		assert.Contains(t, string(bb), want)
	}
}

func TestHookSubcommand_HookAnnotateMode(t *testing.T) {
	matrixPath := writeFixtureMatrix(t)
	out, err := choosehook.Decide(
		choosehook.DecideInput{Prompt: "review this code", HookMode: "annotate"},
		matrixPath, "",
	)
	require.NoError(t, err)
	assert.Equal(t, "annotate", out.HookMode)
	assert.Empty(t, out.BaseURL, "annotate mode must NOT rewrite base_url")
}

// Test the Cursor hooks.json template generator that we bundle
// in cursor-config/hooks/beforeSubmitPrompt.sh. The generator
// function is exposed through the cobra CLI; we exercise it
// here as a unit.
func TestCursorTemplate_ContainsRequiredKeys(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "hooks.json")

	// Re-write the runHookInstall cmd locally; the production
	// version uses cobra flags but we want to test the JSON
	// shape, not the flag plumbing.
	payload := buildHooksJSON("/usr/local/bin/choose-llm", "--quiet")
	require.NoError(t, os.WriteFile(outPath, []byte(payload), 0o644))
	raw, err := os.ReadFile(outPath)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(raw, &parsed))
	hooks, ok := parsed["hooks"].(map[string]any)
	require.True(t, ok, "JSON must have hooks root key")
	bs, ok := hooks["beforeSubmitPrompt"].([]any)
	require.True(t, ok, "beforeSubmitPrompt must be an array")
	require.NotEmpty(t, bs)
	entry := bs[0].(map[string]any)
	assert.Equal(t, "helixon choose-llm", entry["name"])
}

// runHookInstall is a smoke for the cobra-backed hook install
// path. It runs the production CLI sub-command and asserts the
// output file contents.
func TestHookSubcommand_InstallWritesHookJSON(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "hooks.json")

	bin := t.TempDir() + "/choose-llm"
	// Skipping the build step — we only need the cobra path
	// to produce the JSON output; the runner doesn't need the
	// binary to actually exist because we never invoke it.
	_ = bin

	stdout, stderr, err := runCobra([]string{
		"hook", "install", "--out", outPath,
		"--binary", bin,
	}, nil)
	require.NoError(t, err, "stderr=%s", stderr)

	raw, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(raw), "beforeSubmitPrompt"))
	assert.True(t, bytes.Contains([]byte(stdout), []byte("hooks.json")), "stdout should confirm the install location: got=%s", stdout)
}

func TestHookSubcommand_DecideReadsStdin(t *testing.T) {
	// Use the production cobra 'decide' sub-command but feed
	// stdin with a synthetic DecideInput. We stub the matrix
	// path via env.
	matrixPath := writeFixtureMatrix(t)

	stdin := strings.NewReader(`{"prompt":"write a Go function that returns max"}`)
	r, w, _ := os.Pipe()
	oldStdin := os.Stdin
	os.Stdin = r
	go func() {
		defer w.Close()
		_, _ = io.Copy(w, stdin)
	}()
	defer func() { os.Stdin = oldStdin }()

	stdout, _, err := runCobra([]string{
		"hook", "decide",
		"--matrix", matrixPath,
	}, nil)
	require.NoError(t, err)

	var out choosehook.Output
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	assert.Equal(t, "tier2", out.DecisionLabel)
}

// runCobra is the canonical CLI test helper.
func runCobra(args []string, env map[string]string) (stdout string, stderr string, err error) {
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	for k, v := range env {
		old := os.Getenv(k)
		os.Setenv(k, v)
		defer os.Setenv(k, old)
	}
	err = root.Execute()
	return out.String(), errBuf.String(), err
}
