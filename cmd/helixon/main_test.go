package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon/platform"
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

func TestResolvePlatformAddr_FlagWins(t *testing.T) {
	t.Parallel()
	if got := resolvePlatformAddr("10.0.0.1:9999"); got != "10.0.0.1:9999" {
		t.Errorf("flag not honoured: %q", got)
	}
}

func TestResolvePlatformAddr_EnvColonAddr(t *testing.T) {
	t.Setenv("HELIXON_PORT", "192.168.1.5:7777")
	if got := resolvePlatformAddr(""); got != "192.168.1.5:7777" {
		t.Errorf("env colon form: %q", got)
	}
}

func TestResolvePlatformAddr_EnvPortOnly(t *testing.T) {
	t.Setenv("HELIXON_PORT", "8080")
	if got := resolvePlatformAddr(""); got != "127.0.0.1:8080" {
		t.Errorf("env port-only form: %q", got)
	}
}

func TestResolvePlatformAddr_Default(t *testing.T) {
	t.Setenv("HELIXON_PORT", "")
	if got := resolvePlatformAddr(""); got != platform.DefaultAddr {
		t.Errorf("default fallback: %q (want %q)", got, platform.DefaultAddr)
	}
}

func TestLoadConfig_EmptyPathReturnsDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig(\"\"): %v", err)
	}
	if cfg.Logger == nil {
		t.Error("expected default Logger to be set")
	}
	if cfg.AgentID != "" {
		t.Errorf("AgentID should be zero: %q", cfg.AgentID)
	}
}

func TestLoadConfig_BadPathErrors(t *testing.T) {
	t.Parallel()
	_, err := loadConfig("/no/such/path/helixon.yaml")
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestLoadConfig_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "helixon.yaml")
	body := "agent_id: cfg-test\ntimeout: 30s\nheartbeat_every: 10s\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.AgentID != "cfg-test" {
		t.Errorf("AgentID: %q", cfg.AgentID)
	}
	if cfg.Logger == nil {
		t.Error("Logger should be populated")
	}
}

func TestPlatformCmd_FlagBindingResolves(t *testing.T) {
	t.Parallel()
	// Verify resolvePlatformAddr honours --addr when the flag is set.
	// Running the full platform subcommand blocks listening, so we
	// exercise only the resolver which is the testable surface.
	got := resolvePlatformAddr("127.0.0.1:9999")
	if got != "127.0.0.1:9999" {
		t.Errorf("addr flag not honoured: %q", got)
	}
}

func TestServe_BadHeartbeat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "helixon.yaml")
	body := "agent_id: serve-test\ntimeout: 30s\nheartbeat_every: 10s\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := runRoot(t, "serve", "--config", path, "--heartbeat", "not-a-duration")
	if err == nil {
		t.Fatal("expected error for bad heartbeat")
	}
	if !strings.Contains(err.Error(), "heartbeat") {
		t.Errorf("error should mention heartbeat: %v", err)
	}
}

func TestRootCmd_VersionSubcommand(t *testing.T) {
	t.Parallel()
	out, _, err := runRoot(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "helixon") {
		t.Errorf("missing helixon banner: %q", out)
	}
}

func TestTaskCmdMain_RequiresTicketOrPrompt(t *testing.T) {
	t.Parallel()
	_, _, err := runRoot(t, "task")
	if err == nil {
		t.Fatal("expected error when both --ticket and --prompt missing")
	}
	if !strings.Contains(err.Error(), "ticket") && !strings.Contains(err.Error(), "prompt") {
		t.Errorf("error should mention ticket or prompt: %v", err)
	}
}

func TestTaskCmdTruncateHelper(t *testing.T) {
	t.Parallel()
	if got := truncate("hello world", 5); got != "hello..." {
		t.Errorf("truncate: %q", got)
	}
	if got := truncate("hi", 100); got != "hi" {
		t.Errorf("truncate short: %q", got)
	}
}
