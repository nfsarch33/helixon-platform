package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempInventory writes a JSON inventory to a temp dir and points
// HELIXON_CURSOR_TOOLS_INVENTORY at it for the duration of the test.
func withTempInventory(t *testing.T, inv Inventory) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "inv.json")
	body, _ := json.Marshal(inv)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("write inv: %v", err)
	}
	t.Setenv("HELIXON_CURSOR_TOOLS_INVENTORY", p)
	return p
}

func sampleInventory() Inventory {
	return Inventory{
		Version:   1,
		UpdatedAt: "2026-07-15T00:00:00Z",
		Servers: []Server{
			{ID: "ok-server", Command: "/bin/true", Args: []string{}},
			{ID: "missing-server", Command: "definitely-not-on-path", Args: []string{}},
			{ID: "disabled-server", Command: "/bin/true", Disabled: true},
			{ID: "no-command", Command: "", Args: []string{}},
		},
	}
}

func TestLoadInventory_OK(t *testing.T) {
	p := withTempInventory(t, sampleInventory())
	got, err := loadInventory(p)
	if err != nil {
		t.Fatalf("loadInventory: %v", err)
	}
	if got.Version != 1 || len(got.Servers) != 4 {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestLoadInventory_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInventory(p); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadInventory_Missing(t *testing.T) {
	if _, err := loadInventory(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected missing-file error")
	}
}

func TestDefaultPing_OKWhenCommandIsAbsolute(t *testing.T) {
	r := defaultPing(Server{ID: "x", Command: "/bin/true"})
	if r.State != "ok" {
		t.Fatalf("expected ok, got %+v", r)
	}
}

func TestDefaultPing_FailWhenMissing(t *testing.T) {
	r := defaultPing(Server{ID: "x", Command: "this-binary-does-not-exist-xyz123"})
	if r.State != "fail" {
		t.Fatalf("expected fail, got %+v", r)
	}
	if !strings.Contains(r.Reason, "not on PATH") {
		t.Fatalf("expected reason to mention PATH, got %q", r.Reason)
	}
}

func TestDefaultPing_SkippedWhenDisabled(t *testing.T) {
	r := defaultPing(Server{ID: "x", Command: "/bin/true", Disabled: true})
	if r.State != "skipped" || !r.Disabled {
		t.Fatalf("expected skipped+disabled, got %+v", r)
	}
}

func TestDefaultPing_FailWhenNoCommand(t *testing.T) {
	r := defaultPing(Server{ID: "x", Command: ""})
	if r.State != "fail" {
		t.Fatalf("expected fail, got %+v", r)
	}
}

func TestRunDoctor_AggregatesResults(t *testing.T) {
	withTempInventory(t, sampleInventory())
	inv, err := loadInventory(inventoryPath())
	if err != nil {
		t.Fatal(err)
	}
	results := runDoctor(inv, 4)
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	states := map[string]int{}
	for _, r := range results {
		states[r.State]++
	}
	if states["ok"] != 1 {
		t.Fatalf("expected 1 ok, got %d (%v)", states["ok"], states)
	}
	if states["fail"] != 2 {
		t.Fatalf("expected 2 fail (missing-binary + no-command), got %d (%v)", states["fail"], states)
	}
	if states["skipped"] != 1 {
		t.Fatalf("expected 1 skipped, got %d (%v)", states["skipped"], states)
	}
	// Sorted by ID
	for i := 1; i < len(results); i++ {
		if results[i-1].ID > results[i].ID {
			t.Fatalf("results not sorted by ID: %v", ids(results))
		}
	}
}

func TestBuildCursorSnippet_Roundtrip(t *testing.T) {
	s := Server{
		ID:      "github-official",
		Command: "docker",
		Args:    []string{"run", "-i", "--rm", "ghcr.io/github/github-mcp-server"},
		Env:     map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "$GITHUB_PERSONAL_ACCESS_TOKEN"},
	}
	snippet := buildCursorSnippet(s)
	if !strings.Contains(snippet, `"mcpServers"`) {
		t.Fatalf("missing mcpServers wrapper: %s", snippet)
	}
	if !strings.Contains(snippet, `"github-official"`) {
		t.Fatalf("missing server id: %s", snippet)
	}
	if !strings.Contains(snippet, `"ghcr.io/github/github-mcp-server"`) {
		t.Fatalf("missing args: %s", snippet)
	}

	// Round-trip via json
	var wrapped map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(snippet), &wrapped); err != nil {
		t.Fatalf("snippet not valid json: %v\n%s", err, snippet)
	}
	entry, ok := wrapped["mcpServers"]["github-official"]
	if !ok {
		t.Fatalf("missing mcpServers.github-official")
	}
	if entry["command"] != "docker" {
		t.Fatalf("command mismatch: %v", entry["command"])
	}
}

func TestBuildCursorSnippet_DisabledPropagates(t *testing.T) {
	s := Server{ID: "x", Command: "npx", Args: []string{"-y", "x"}, Disabled: true}
	snippet := buildCursorSnippet(s)
	if !strings.Contains(snippet, `"disabled": true`) {
		t.Fatalf("missing disabled flag: %s", snippet)
	}
}

func TestInventoryPath_Override(t *testing.T) {
	t.Setenv("HELIXON_CURSOR_TOOLS_INVENTORY", "/custom/inv.json")
	if got := inventoryPath(); got != "/custom/inv.json" {
		t.Fatalf("expected /custom/inv.json, got %q", got)
	}
}

func ids(rs []DoctorResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestRunMain_NoArgs(t *testing.T) {
	if code := runMain(nil); code != 3 {
		t.Errorf("expected 3 for no args, got %d", code)
	}
}

func TestRunMain_Unknown(t *testing.T) {
	if code := runMain([]string{"nope"}); code != 3 {
		t.Errorf("expected 3 for unknown, got %d", code)
	}
}

func TestRunMain_Version(t *testing.T) {
	if code := runMain([]string{"version"}); code != 0 {
		t.Errorf("expected 0 for version, got %d", code)
	}
}

func TestRunMain_Help(t *testing.T) {
	if code := runMain([]string{"help"}); code != 0 {
		t.Errorf("expected 0 for help, got %d", code)
	}
}

func TestRunMain_List(t *testing.T) {
	withTempInventory(t, sampleInventory())
	if code := runMain([]string{"list"}); code != 0 {
		t.Errorf("expected 0 for list, got %d", code)
	}
}

func TestRunMain_List_MissingInv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELIXON_CURSOR_TOOLS_INVENTORY", filepath.Join(dir, "missing.json"))
	if code := runMain([]string{"list"}); code != 2 {
		t.Errorf("expected 2 for missing inv, got %d", code)
	}
}

func TestRunMain_Doctor_OK(t *testing.T) {
	withTempInventory(t, sampleInventory())
	if code := runMain([]string{"doctor", "--json"}); code != 1 {
		t.Errorf("expected 1 (failures), got %d", code)
	}
}

func TestRunMain_Doctor_AllOK(t *testing.T) {
	inv := Inventory{
		Version: 1,
		Servers: []Server{
			{ID: "ok1", Command: "/bin/true"},
			{ID: "ok2", Command: "/bin/true"},
		},
	}
	withTempInventory(t, inv)
	if code := runMain([]string{"doctor"}); code != 0 {
		t.Errorf("expected 0 (no failures), got %d", code)
	}
}

func TestRunMain_Doctor_BadFlag(t *testing.T) {
	if code := runMain([]string{"doctor", "--bogus"}); code != 3 {
		t.Errorf("expected 3 for bad flag, got %d", code)
	}
}

func TestRunMain_Doctor_MissingInv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELIXON_CURSOR_TOOLS_INVENTORY", filepath.Join(dir, "missing.json"))
	if code := runMain([]string{"doctor"}); code != 2 {
		t.Errorf("expected 2 for missing inv, got %d", code)
	}
}

func TestRunMain_Restore_MissingServer(t *testing.T) {
	withTempInventory(t, sampleInventory())
	if code := runMain([]string{"restore"}); code != 3 {
		t.Errorf("expected 3 for missing --server, got %d", code)
	}
}

func TestRunMain_Restore_NotFound(t *testing.T) {
	withTempInventory(t, sampleInventory())
	if code := runMain([]string{"restore", "--server", "nope"}); code != 3 {
		t.Errorf("expected 3 for not found, got %d", code)
	}
}

func TestRunMain_Restore_Found(t *testing.T) {
	withTempInventory(t, sampleInventory())
	if code := runMain([]string{"restore", "--server", "ok-server"}); code != 0 {
		t.Errorf("expected 0 for found, got %d", code)
	}
}

func TestRunMain_Restore_Found_WithOut(t *testing.T) {
	withTempInventory(t, sampleInventory())
	dir := t.TempDir()
	out := filepath.Join(dir, "out.json")
	if code := runMain([]string{"restore", "--server", "ok-server", "--out", out}); code != 0 {
		t.Errorf("expected 0, got %d", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output file not created: %v", err)
	}
}

func TestRunMain_Restore_BadOutPath(t *testing.T) {
	withTempInventory(t, sampleInventory())
	if code := runMain([]string{"restore", "--server", "ok-server", "--out", "/nonexistent-dir/x.json"}); code != 2 {
		t.Errorf("expected 2 for bad out path, got %d", code)
	}
}

func TestRunMain_Restore_MissingInv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELIXON_CURSOR_TOOLS_INVENTORY", filepath.Join(dir, "missing.json"))
	if code := runMain([]string{"restore", "--server", "x"}); code != 2 {
		t.Errorf("expected 2 for missing inv, got %d", code)
	}
}

func TestRunMain_Config_OK(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(cfg, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELIXON_CURSOR_TOOLS_CONFIG", cfg)
	if code := runMain([]string{"config"}); code != 0 {
		t.Errorf("expected 0 for config, got %d", code)
	}
}

func TestRunMain_Config_Missing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELIXON_CURSOR_TOOLS_CONFIG", filepath.Join(dir, "missing.json"))
	if code := runMain([]string{"config"}); code != 2 {
		t.Errorf("expected 2 for missing config, got %d", code)
	}
}

func TestCountState(t *testing.T) {
	rs := []DoctorResult{
		{State: "ok"},
		{State: "ok"},
		{State: "fail"},
		{State: "skipped"},
	}
	if got := countState(rs, "ok"); got != 2 {
		t.Errorf("countState ok=%d", got)
	}
	if got := countState(rs, "fail"); got != 1 {
		t.Errorf("countState fail=%d", got)
	}
	if got := countState(rs, "skipped"); got != 1 {
		t.Errorf("countState skipped=%d", got)
	}
	if got := countState(rs, "missing"); got != 0 {
		t.Errorf("countState missing=%d", got)
	}
}

func TestConfigPath_Override(t *testing.T) {
	t.Setenv("HELIXON_CURSOR_TOOLS_CONFIG", "/custom/cfg.json")
	if got := configPath(); got != "/custom/cfg.json" {
		t.Errorf("expected /custom/cfg.json, got %q", got)
	}
}

func TestLookPath_AbsoluteFile(t *testing.T) {
	p, err := lookPath("/bin/true")
	if err != nil || p == "" {
		t.Errorf("expected to find /bin/true, got p=%q err=%v", p, err)
	}
}

func TestLookPath_AbsoluteFile_Missing(t *testing.T) {
	if _, err := lookPath("/nonexistent-bin-xyz123"); err == nil {
		t.Error("expected error for missing absolute file")
	}
}

func TestLookPath_RelativeFile(t *testing.T) {
	p, err := lookPath("./relative-file-xyz-not-exist")
	if err == nil || p != "" {
		t.Errorf("expected error for missing relative file, got p=%q err=%v", p, err)
	}
}

func TestLookPath_OnPath(t *testing.T) {
	// /bin/true should be on PATH
	if _, err := lookPath("true"); err != nil {
		t.Skipf("true not on PATH for this test: %v", err)
	}
}

func TestFindOnPath_Empty(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := findOnPath("definitely-not-here"); err == nil {
		t.Error("expected error for empty PATH lookup")
	}
}
