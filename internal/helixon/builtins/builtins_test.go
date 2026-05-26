package builtins_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

func TestShellTool_AllowList_v8900(t *testing.T) {
	reg := tooldispatch.NewRegistry(nil)
	if err := reg.Register(builtins.ShellTool(builtins.ShellConfig{
		AllowedCommands: []string{"echo"},
		Timeout:         3 * time.Second,
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	out, err := reg.Execute(context.Background(), "shell", `{"command":"echo","args":["hello v8900"]}`)
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if !strings.Contains(out, "hello v8900") {
		t.Fatalf("echo output: %q", out)
	}

	if _, err := reg.Execute(context.Background(), "shell", `{"command":"rm","args":["-rf","/"]}`); err == nil {
		t.Fatal("rm must be blocked by allow-list")
	}
}

func TestWebFetchTool_GET_v8900(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok-body"))
	}))
	defer srv.Close()

	reg := tooldispatch.NewRegistry(nil)
	if err := reg.Register(builtins.WebFetchTool(builtins.WebFetchConfig{
		HTTPClient: srv.Client(),
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	argsJSON, _ := json.Marshal(map[string]any{"url": srv.URL + "/x"})
	out, err := reg.Execute(context.Background(), "web_fetch", string(argsJSON))
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"].(float64) != 200 {
		t.Fatalf("status: %v", got["status"])
	}
	if got["body"].(string) != "ok-body" {
		t.Fatalf("body: %q", got["body"])
	}

	if _, err := reg.Execute(context.Background(), "web_fetch", `{"url":"file:///etc/passwd"}`); err == nil {
		t.Fatal("file:// scheme must be rejected")
	}
}

func TestSprintboardTool_RegisterClaim_v8900(t *testing.T) {
	var registers, claims int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agents":
			registers++
			w.WriteHeader(200)
		case "/api/v1/tickets/T-1/claim":
			claims++
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "test-agent",
	}, nil)

	reg := tooldispatch.NewRegistry(nil)
	if err := reg.Register(builtins.SprintboardTool(client)); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := reg.Execute(context.Background(), "sprintboard", `{"op":"register"}`); err != nil {
		t.Fatalf("register op: %v", err)
	}
	if _, err := reg.Execute(context.Background(), "sprintboard", `{"op":"claim","ticket_id":"T-1"}`); err != nil {
		t.Fatalf("claim op: %v", err)
	}
	if registers != 1 || claims != 1 {
		t.Fatalf("counts: registers=%d claims=%d", registers, claims)
	}
}

func TestFileReadTool_ReadAndTruncate(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "read_test.txt")
	if err := os.WriteFile(testFile, []byte("hello helixon"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tooldispatch.NewRegistry(nil)
	if err := reg.Register(builtins.FileReadTool(builtins.FileReadConfig{})); err != nil {
		t.Fatalf("register: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": testFile})
	out, err := reg.Execute(context.Background(), "file_read", string(args))
	if err != nil {
		t.Fatalf("file_read: %v", err)
	}
	if out != "hello helixon" {
		t.Fatalf("expected 'hello helixon', got %q", out)
	}

	if _, err := reg.Execute(context.Background(), "file_read", `{"path":"/nonexistent/path"}`); err == nil {
		t.Fatal("reading nonexistent file must fail")
	}
}

func TestFileReadTool_AllowedPaths(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "allowed.txt")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tooldispatch.NewRegistry(nil)
	if err := reg.Register(builtins.FileReadTool(builtins.FileReadConfig{
		AllowedPaths: []string{tmpDir},
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": testFile})
	out, err := reg.Execute(context.Background(), "file_read", string(args))
	if err != nil {
		t.Fatalf("file_read allowed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("expected 'ok', got %q", out)
	}

	if _, err := reg.Execute(context.Background(), "file_read", `{"path":"/etc/passwd"}`); err == nil {
		t.Fatal("reading outside allowed paths must fail")
	}
}

func TestFileWriteTool_WriteAndVerify(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "subdir", "write_test.txt")

	reg := tooldispatch.NewRegistry(nil)
	if err := reg.Register(builtins.FileWriteTool(builtins.FileWriteConfig{})); err != nil {
		t.Fatalf("register: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": testFile, "content": "written by helixon"})
	out, err := reg.Execute(context.Background(), "file_write", string(args))
	if err != nil {
		t.Fatalf("file_write: %v", err)
	}
	if !strings.Contains(out, "18 bytes") {
		t.Fatalf("expected byte count in output, got %q", out)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "written by helixon" {
		t.Fatalf("file content: %q", string(data))
	}
}

func TestFileWriteTool_AllowedPaths(t *testing.T) {
	tmpDir := t.TempDir()

	reg := tooldispatch.NewRegistry(nil)
	if err := reg.Register(builtins.FileWriteTool(builtins.FileWriteConfig{
		AllowedPaths: []string{tmpDir},
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := reg.Execute(context.Background(), "file_write",
		`{"path":"/tmp/should_not_exist.txt","content":"nope"}`); err == nil {
		t.Fatal("writing outside allowed paths must fail")
	}
}

func TestRegisterAll_StableOrder_v8900(t *testing.T) {
	opts := builtins.Options{
		Shell: &builtins.ShellConfig{AllowedCommands: []string{"echo"}},
		WebFetch: &builtins.WebFetchConfig{
			HTTPClient: &http.Client{Timeout: time.Second},
		},
	}
	defs := opts.Defs()
	if len(defs) != 2 {
		t.Fatalf("expected 2 defs, got %d", len(defs))
	}
	if defs[0].Name != "shell" || defs[1].Name != "web_fetch" {
		t.Fatalf("unexpected order: %s, %s", defs[0].Name, defs[1].Name)
	}
}

func TestRegisterAll_WithFileTools(t *testing.T) {
	opts := builtins.Options{
		Shell:     &builtins.ShellConfig{AllowedCommands: []string{"echo"}},
		FileRead:  &builtins.FileReadConfig{},
		FileWrite: &builtins.FileWriteConfig{},
	}
	defs := opts.Defs()
	if len(defs) != 3 {
		t.Fatalf("expected 3 defs, got %d", len(defs))
	}
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	expected := []string{"file_read", "file_write", "shell"}
	for i, n := range expected {
		if names[i] != n {
			t.Fatalf("index %d: expected %q, got %q", i, n, names[i])
		}
	}
}
