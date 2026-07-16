// Tests for cmd/registra/main.go. Captures the existing dispatch behavior
// of every subcommand so that the v17714-1 CC-reduction refactor can move
// implementation into helper functions without changing observable output.
//
// TDD pattern:
//  1. Tests are written first against the unrefactored main (RED — runRegistra
//     does not yet exist).
//  2. main() is extracted to runRegistra(args []string) (GREEN — baseline
//     captured).
//  3. Dispatch body is moved into per-subcommand helpers; tests stay GREEN.
//
// See reports/research/sprint-v17714-hygiene-kpi.md for full TDD log.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nfsarch33/helixon-platform/internal/registra"
)

// runCapturing invokes runRegistra(args) with stdout/stderr redirected to
// returned buffers. It mirrors the production pipeline but avoids os.Exit so
// the calling test can assert on captured output.
func runCapturing(t *testing.T, args []string) (rc int, stdout, stderr string) {
	t.Helper()
	origStdout, origStderr := os.Stdout, os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	er, ew, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stdout, os.Stderr = pw, ew
	defer func() { os.Stdout, os.Stderr = origStdout, origStderr }()

	doneCh := make(chan struct{})
	var stdoutBuf, stderrBuf bytes.Buffer
	go func() {
		_, _ = io.Copy(&stdoutBuf, pr)
		close(doneCh)
	}()
	var stderrDoneCh = make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderrBuf, er)
		close(stderrDoneCh)
	}()

	rc = runRegistra(args)

	_ = pw.Close()
	_ = ew.Close()
	<-doneCh
	<-stderrDoneCh
	return rc, stdoutBuf.String(), stderrBuf.String()
}

// fixtureRegistry writes a minimal registry.yaml with one service, one node,
// and one LLM cell so subcommands that read the registry have something to
// print. It returns the registry directory (parent of the file).
func fixtureRegistry(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	reg := registra.Registry{
		RegistryVersion: "v1",
		SchemaVersion:   1,
		CentralNode:     "wsl2",
		Services: []registra.Service{
			{Name: "alpha", Kind: "core", PrimaryNode: "wsl2", Address: "127.0.0.1", Port: 8001, HealthPath: "/healthz", OwnerSprint: "v17714"},
		},
		Nodes: []registra.Node{
			{Alias: "wsl2", CanonicalHostname: "wsl2.local", TailscaleIP: "100.64.0.2", Role: "central", User: "jason", SSHPort: 22},
		},
		LLMCells: []registra.LLMCell{
			{CellID: "cell-qwen", Node: "wsl2", GPUClass: "RTX-4070", ModelID: "qwen3.7", Engine: "vllm", HostPort: 8001, Status: "ready"},
		},
	}
	if err := writeRegistryYAML(filepath.Join(dir, "registry.yaml"), reg); err != nil {
		t.Fatalf("save fixture registry: %v", err)
	}
	return dir
}

func writeRegistryYAML(path string, reg registra.Registry) error {
	data, err := yaml.Marshal(&reg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func TestRunRegistra_Usage(t *testing.T) {
	rc, _, stderr := runCapturing(t, []string{"--registry", "/dev/null"})
	if rc != 2 {
		t.Errorf("missing subcommand: rc = %d; want 2", rc)
	}
	if !strings.Contains(stderr, "list") {
		t.Errorf("usage text missing in stderr: %q", stderr)
	}
}

func TestRunRegistra_UnknownSubcommand(t *testing.T) {
	rc, _, stderr := runCapturing(t, []string{"bogus", "--registry", "/dev/null"})
	if rc != 2 {
		t.Errorf("unknown subcommand: rc = %d; want 2", rc)
	}
	if !strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("expected stderr to mention unknown subcommand; got %q", stderr)
	}
}

func TestRunRegistra_List(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"list", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("list: rc = %d", rc)
	}
	// Header is fixed; body contains service name.
	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "PORT") {
		t.Errorf("list header missing: %q", stdout)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Errorf("list body missing service 'alpha': %q", stdout)
	}
}

func TestRunRegistra_ListFilteredByKind(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"list", "--kind", "core", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("list --kind: rc = %d", rc)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Errorf("expected 'alpha' for kind=core; got %q", stdout)
	}
}

func TestRunRegistra_ListFilteredByKind_NoMatch(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"list", "--kind", "nope", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("list --kind=nope: rc = %d", rc)
	}
	// Header still prints; no body row should match.
	if strings.Contains(stdout, "alpha") {
		t.Errorf("list --kind=nope should not contain 'alpha': %q", stdout)
	}
}

func TestRunRegistra_ListFilteredByNode(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"list", "--node", "wsl2", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("list --node: rc = %d", rc)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Errorf("list --node=wsl2 should include 'alpha': %q", stdout)
	}
}

func TestRunRegistra_Show(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"show", "alpha", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("show: rc = %d", rc)
	}
	// MarshalIndent output is JSON; assert key fields parsed.
	var got registra.Service
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("show JSON unmarshal: %v\n%s", err, stdout)
	}
	if got.Name != "alpha" || got.Port != 8001 {
		t.Errorf("unexpected service: %+v", got)
	}
}

func TestRunRegistra_ShowMissingArg(t *testing.T) {
	rc, _, _ := runCapturing(t, []string{"show", "--registry", "/dev/null"})
	if rc != 2 {
		t.Errorf("show missing arg: rc = %d; want 2", rc)
	}
}

func TestRunRegistra_ShowUnknown(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, _, stderr := runCapturing(t, []string{"show", "nope", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 1 {
		t.Errorf("show unknown: rc = %d; want 1", rc)
	}
	if !strings.Contains(stderr, `no service "nope"`) {
		t.Errorf("expected stderr to mention missing service; got %q", stderr)
	}
}

func TestRunRegistra_Nodes(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"nodes", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("nodes: rc = %d", rc)
	}
	if !strings.Contains(stdout, "ALIAS") {
		t.Errorf("nodes header missing: %q", stdout)
	}
	if !strings.Contains(stdout, "wsl2") {
		t.Errorf("nodes body missing 'wsl2': %q", stdout)
	}
}

func TestRunRegistra_Cells(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"cells", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("cells: rc = %d", rc)
	}
	if !strings.Contains(stdout, "CELL") {
		t.Errorf("cells header missing: %q", stdout)
	}
	if !strings.Contains(stdout, "cell-qwen") {
		t.Errorf("cells body missing 'cell-qwen': %q", stdout)
	}
}

func TestRunRegistra_Credential(t *testing.T) {
	// Build a credential-bearing registry. The fixture has none, so build
	// one inline by t.TempDir + writeRegistryYAML.
	dir := t.TempDir()
	reg := registra.Registry{
		RegistryVersion: "v1",
		SchemaVersion:   1,
		CentralNode:     "wsl2",
		CredentialsIndex: []registra.Credential{{
			ID: "abc1234567890zyxwvu-tsrq", Title: "My Item",
			Vault: "HelixonSafe", Category: "API_KEY", OPURI: "op://HelixonSafe/My Item/password",
		}},
	}
	if err := writeRegistryYAML(filepath.Join(dir, "registry.yaml"), reg); err != nil {
		t.Fatalf("save: %v", err)
	}
	rc, stdout, _ := runCapturing(t, []string{"credential", "My Item", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("credential: rc = %d", rc)
	}
	if !strings.Contains(stdout, "id=") || !strings.Contains(stdout, "title=My Item") {
		t.Errorf("credential output unexpected: %q", stdout)
	}
}

func TestRunRegistra_Credential_MissingArg(t *testing.T) {
	rc, _, _ := runCapturing(t, []string{"credential", "--registry", "/dev/null"})
	if rc != 2 {
		t.Errorf("credential missing arg: rc = %d; want 2", rc)
	}
}

func TestRunRegistra_Credential_Unknown(t *testing.T) {
	dir := t.TempDir()
	reg := registra.Registry{RegistryVersion: "v1", SchemaVersion: 1, CentralNode: "wsl2"}
	if err := writeRegistryYAML(filepath.Join(dir, "registry.yaml"), reg); err != nil {
		t.Fatalf("save: %v", err)
	}
	rc, _, stderr := runCapturing(t, []string{"credential", "nope", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 1 {
		t.Errorf("credential unknown: rc = %d; want 1", rc)
	}
	if !strings.Contains(stderr, `no item "nope"`) {
		t.Errorf("expected stderr to mention missing item; got %q", stderr)
	}
}

func TestRunRegistra_Summary(t *testing.T) {
	dir := fixtureRegistry(t)
	rc, stdout, _ := runCapturing(t, []string{"summary", "--registry", filepath.Join(dir, "registry.yaml")})
	if rc != 0 {
		t.Fatalf("summary: rc = %d", rc)
	}
	for _, want := range []string{"registry_version", "central_node", "services", "nodes", "llm_cells"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("summary missing %q: %s", want, stdout)
		}
	}
}

func TestRunRegistra_Help(t *testing.T) {
	dir := fixtureRegistry(t)
	for _, arg := range []string{"-h", "--help", "help"} {
		t.Run(arg, func(t *testing.T) {
			rc, stdout, _ := runCapturing(t, []string{arg, "--registry", filepath.Join(dir, "registry.yaml")})
			if rc != 0 {
				t.Errorf("%s: rc = %d; want 0", arg, rc)
			}
			if !strings.Contains(stdout, "list") || !strings.Contains(stdout, "show") {
				t.Errorf("%s body missing subcommand list: %q", arg, stdout)
			}
		})
	}
}

func TestRunRegistra_BadRegistryPath(t *testing.T) {
	rc, _, stderr := runCapturing(t, []string{"summary", "--registry", "/nonexistent/registry.yaml"})
	if rc != 1 {
		t.Errorf("bad registry: rc = %d; want 1", rc)
	}
	if !strings.Contains(stderr, "registra:") {
		t.Errorf("expected stderr 'registra:' prefix; got %q", stderr)
	}
}

// TestRunRegistra_RegistryFlagForms exercises both --registry value and
// --registry=value forms. This pins down the two-pass argv parsing logic.
func TestRunRegistra_RegistryFlagForms(t *testing.T) {
	dir := fixtureRegistry(t)
	for _, args := range [][]string{
		{"summary", "--registry", filepath.Join(dir, "registry.yaml")},
		{"summary", "--registry=" + filepath.Join(dir, "registry.yaml")},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			rc, _, _ := runCapturing(t, args)
			if rc != 0 {
				t.Errorf("args %v: rc = %d; want 0", args, rc)
			}
		})
	}
}
