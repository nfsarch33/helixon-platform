package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// freeTCPAddr returns a TCP address bound to an ephemeral port. The
// listener is closed before returning so the caller can rebind on the
// same port. Used by the platform healthz/readyz subprocess test.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestPlatform_ExposesHealthzAndReadyz_v14509 pins the v14509 Sentrux
// pair-3 closeout: the production binary's `helixon platform` subcommand
// must expose /healthz and /readyz on the bound address so k8s liveness/
// readiness probes can wire in. This closes the v14506 plan-vs-reality
// gap noted in session-handoffs/v14506-handoff.md.
//
// The test spawns the actual binary as a subprocess (so the cobra root
// + signal.NotifyContext path is exercised) and curls both endpoints.
func TestPlatform_ExposesHealthzAndReadyz_v14509(t *testing.T) {
	addr := freeTCPAddr(t)
	bin := filepath.Join(t.TempDir(), "helixon")
	if err := exec.Command("go", "build", "-o", bin, "../../cmd/helixon").Run(); err != nil {
		t.Fatalf("go build helixon: %v", err)
	}
	cmd := exec.Command(bin, "platform", "--addr", addr)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
	}()

	// Wait up to 3 s for the listener to come up.
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	var healthResp *http.Response
	var healthErr error
	for time.Now().Before(deadline) {
		healthResp, healthErr = client.Get("http://" + addr + "/healthz")
		if healthErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if healthErr != nil {
		t.Fatalf("GET /healthz never succeeded within 3 s: %v", healthErr)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status=%d want 200", healthResp.StatusCode)
	}
	var healthBody map[string]any
	if err := json.NewDecoder(healthResp.Body).Decode(&healthBody); err != nil {
		t.Fatalf("decode /healthz: %v", err)
	}
	if status, _ := healthBody["status"].(string); status != "ok" {
		t.Errorf("/healthz status=%q want ok (full=%v)", status, healthBody)
	}

	readyResp, err := client.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz status=%d want 200", readyResp.StatusCode)
	}
	var readyBody map[string]any
	if err := json.NewDecoder(readyResp.Body).Decode(&readyBody); err != nil {
		t.Fatalf("decode /readyz: %v", err)
	}
	// /readyz may report either {"ready":true} (no gate) or {"ready":false,"reason":...}.
	if _, ok := readyBody["ready"]; !ok {
		t.Errorf("/readyz missing 'ready' field: %v", readyBody)
	}
}
