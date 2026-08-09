// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultDoctorScript_NonEmpty(t *testing.T) {
	s := defaultDoctorScript()
	assert.NotEmpty(t, s)
}

func TestDoctor_RunsScriptAndCapturesOutput(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-doctor.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/usr/bin/env bash\necho hello world\nexit 0\n"), 0o755))

	res, err := Doctor(context.Background(), DoctorOptions{
		ScriptPath: script,
		Logger:     nopLogger(),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, res.Stdout, "hello world")
}

func TestDoctor_ReportsFailure(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fail-doctor.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/usr/bin/env bash\necho boom 1>&2\nexit 7\n"), 0o755))

	res, err := Doctor(context.Background(), DoctorOptions{
		ScriptPath: script,
		Logger:     nopLogger(),
	})
	require.Error(t, err)
	assert.Equal(t, 7, res.ExitCode)
	assert.Contains(t, res.Stderr, "boom")
}
