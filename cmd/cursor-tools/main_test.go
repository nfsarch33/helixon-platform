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