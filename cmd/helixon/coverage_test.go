// Coverage tests for cmd/helixon — closes CARRY-058 (cmd/helixon
// coverage 42.9% -> 70%+) for v16204. These tests exercise the bind
// resolution helper extracted from newPlatformCmd and the heartbeat
// parsing error path in newServeCmd.
package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// CARRY-058: resolvePlatformAddr bind-resolution helper.
// Extracted from newPlatformCmd so the precedence logic is testable
// without binding a real listener.
func TestResolvePlatformAddr_FlagWinsEverything(t *testing.T) {
	t.Setenv("HELIXON_PORT", "9999")
	got := resolvePlatformAddr("10.0.0.1:7777")
	if got != "10.0.0.1:7777" {
		t.Fatalf("flag must win; got %q", got)
	}
}

func TestResolvePlatformAddr_EnvWithColon(t *testing.T) {
	t.Setenv("HELIXON_PORT", ":4567")
	got := resolvePlatformAddr("")
	if got != ":4567" {
		t.Fatalf("env with colon must pass through; got %q", got)
	}
}

func TestResolvePlatformAddr_EnvWithoutColon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("loopback prefix differs on windows")
	}
	t.Setenv("HELIXON_PORT", "4567")
	got := resolvePlatformAddr("")
	if got != "127.0.0.1:4567" {
		t.Fatalf("env without colon must be wrapped in 127.0.0.1; got %q", got)
	}
}

func TestResolvePlatformAddr_DefaultWhenNoFlagOrEnv(t *testing.T) {
	t.Setenv("HELIXON_PORT", "")
	got := resolvePlatformAddr("")
	if got == "" {
		t.Fatalf("default must be non-empty (platform.DefaultAddr); got empty")
	}
}

// CARRY-058: serve --heartbeat parsing error path.
// We do NOT call serve (which would build a runtime and listen); we
// only need to confirm the bad-duration branch in newServeCmd.RunE
// returns a wrapped error before any heavy work.
func TestServe_InvalidHeartbeatErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/helixon.yaml"
	if err := writeFile(path, "agent_id: cov-test\ntimeout: 1m\nheartbeat_every: 30s\nprovider:\n  type: echo\n"); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out, errOut, err := runRoot(t, "serve", "--config", path, "--heartbeat", "not-a-duration")
	if err == nil {
		t.Fatalf("expected error for invalid --heartbeat; out=%q errOut=%q", out, errOut)
	}
	if !strings.Contains(err.Error(), "invalid --heartbeat") {
		t.Fatalf("error must mention invalid --heartbeat: %v", err)
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644) //nolint:gosec // G306 test fixture
}
