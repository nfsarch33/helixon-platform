package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper: run the root command with the given args and return stdout/stderr.
func runRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestVersion_PrintsBanner(t *testing.T) {
	t.Parallel()
	out, _, err := runRoot(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "helixon") {
		t.Errorf("version banner missing helixon: %q", out)
	}
}

func TestDoctor_LoadsConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "helixon.yaml")
	body := "agent_id: test\ntimeout: 1m\nheartbeat_every: 30s\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, _, err := runRoot(t, "doctor", "--config", path)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out, "agent_id:") {
		t.Errorf("missing agent_id line: %q", out)
	}
	if !strings.Contains(out, "1m0s") {
		t.Errorf("missing parsed timeout: %q", out)
	}
}

func TestDoctor_NoConfigSucceeds(t *testing.T) {
	t.Parallel()
	out, _, err := runRoot(t, "doctor")
	if err != nil {
		t.Fatalf("doctor (no config): %v", err)
	}
	if !strings.Contains(out, "(none provided") {
		t.Errorf("expected no-config notice, got %q", out)
	}
}

func TestDoctor_BadConfigErrors(t *testing.T) {
	t.Parallel()
	_, _, err := runRoot(t, "doctor", "--config", "/does/not/exist.yaml")
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestServe_RequiresConfig(t *testing.T) {
	t.Parallel()
	_, _, err := runRoot(t, "serve")
	if err == nil {
		t.Fatal("expected error when --config missing")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Errorf("error should mention config: %v", err)
	}
}

func TestRepl_ExitsOnQuit(t *testing.T) {
	t.Parallel()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("hello\n:quit\n"))
	cmd.SetArgs([]string{"repl"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("repl: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "echo: hello") {
		t.Errorf("expected echoed line, got %q", got)
	}
}
