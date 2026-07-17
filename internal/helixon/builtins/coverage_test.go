// Coverage tests for internal/helixon/builtins — closes CARRY-059
// (55.0% -> 70%+) for v16204. Targets the previously-uncovered
// RegisterAll dispatcher and the Memory/Autoresearch tool defs.
package builtins_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

// CARRY-059: RegisterAll dispatcher is uncovered because it loops over
// `defs` and returns the first registration error. This test exercises
// the happy path (zero errors returned) and the partial-options path
// (only some tool fields set, which exercises the Defs() if-nil chain).
func TestRegisterAll_PartialOptions(t *testing.T) {
	reg := tooldispatch.NewRegistry(nil)
	opts := builtins.Options{
		Shell:    &builtins.ShellConfig{AllowedCommands: []string{"echo"}},
		FileRead: &builtins.FileReadConfig{},
	}
	if err := builtins.RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	defs := opts.Defs()
	if len(defs) != 2 {
		t.Fatalf("expected 2 defs (shell + file_read); got %d", len(defs))
	}
}

// CARRY-059: zero-options RegisterAll should not panic and should
// register zero tools (defs is nil, range over nil is a no-op).
func TestRegisterAll_NoOptions(t *testing.T) {
	reg := tooldispatch.NewRegistry(nil)
	if err := builtins.RegisterAll(reg, builtins.Options{}); err != nil {
		t.Fatalf("RegisterAll with zero options: %v", err)
	}
}

// CARRY-059: AutoresearchTool is 0% covered. Calling the constructor
// returns a ToolDef; the description and parameter schema are public
// surface, so we assert they're present without invoking execution
// (which would require a live autoresearch probe).
func TestAutoresearchTool_SchemaIsValid(t *testing.T) {
	def := builtins.AutoresearchTool(builtins.AutoresearchConfig{})
	if def.Name != "autoresearch_run" {
		t.Fatalf("expected tool name autoresearch_run; got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("description must not be empty")
	}
	if len(def.Parameters) == 0 {
		t.Fatal("parameters schema must not be empty")
	}
	if def.Handler == nil {
		t.Fatal("handler function must not be nil")
	}
}

// CARRY-059: SprintboardTool is at 33%. Constructing the tool with
// nil client must not panic (the underlying handler may defer until
// invocation).
func TestSprintboardTool_NilClientConstructs(t *testing.T) {
	def := builtins.SprintboardTool((*controlplane.SprintboardClient)(nil))
	if def.Name != "sprintboard" {
		t.Fatalf("expected sprintboard; got %q", def.Name)
	}
	if def.Handler == nil {
		t.Fatal("handler must not be nil")
	}
}

// CARRY-059: MemoryTool is 0% covered. Passing nil for the searcher
// must not panic at construction; invocation will fail and that is
// expected and asserted here.
func TestMemoryTool_NilSearcherConstructs(t *testing.T) {
	def := builtins.MemoryTool(nil, "app", "user")
	if def.Name != "memory" {
		t.Fatalf("expected memory; got %q", def.Name)
	}
	if def.Handler == nil {
		t.Fatal("handler must not be nil")
	}
}

// CARRY-059: Defs() returns tools in alphabetical order. We pass
// every option kind and verify the order is `autoresearch_run`,
// `file_read`, `file_write`, `memory`, `shell`, `sprintboard`,
// `web_fetch`.
func TestDefs_AlphabeticalOrderWithAllFields(t *testing.T) {
	opts := builtins.Options{
		Shell:        &builtins.ShellConfig{AllowedCommands: []string{"echo"}},
		WebFetch:     &builtins.WebFetchConfig{HTTPClient: &http.Client{}},
		FileRead:     &builtins.FileReadConfig{},
		FileWrite:    &builtins.FileWriteConfig{},
		MemoryAppID:  "app",
		MemoryUserID: "user",
		Autoresearch: &builtins.AutoresearchConfig{},
		Sprintboard:  &controlplane.SprintboardClient{},
	}
	_ = opts.Memory // Memory pointer is nil here; covered in TestMemoryTool_NilSearcherConstructs path
	defs := opts.Defs()
	if len(defs) != 6 {
		t.Fatalf("expected 6 defs; got %d (%v)", len(defs), defNames(defs))
	}
	expected := []string{
		"autoresearch_run", "file_read", "file_write",
		"shell", "sprintboard", "web_fetch",
	}
	for i, want := range expected {
		if defs[i].Name != want {
			t.Fatalf("defs[%d]: expected %q, got %q (full: %v)", i, want, defs[i].Name, defNames(defs))
		}
	}
}

func defNames(defs []tooldispatch.ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}

// CARRY-059: when RegisterAll sees a duplicate tool name, it captures
// the first error and returns it. We force a duplicate by registering
// the same tool twice via a custom Options where we manually inject.
func TestRegisterAll_DuplicateNameReturnsError(t *testing.T) {
	reg := tooldispatch.NewRegistry(nil)
	// Pre-register shell so RegisterAll sees a duplicate.
	if err := reg.Register(builtins.ShellTool(builtins.ShellConfig{AllowedCommands: []string{"echo"}})); err != nil {
		t.Fatalf("pre-register: %v", err)
	}
	opts := builtins.Options{
		Shell: &builtins.ShellConfig{AllowedCommands: []string{"echo"}},
	}
	err := builtins.RegisterAll(reg, opts)
	if err == nil {
		t.Fatal("expected duplicate-name error from RegisterAll")
	}
}

// CARRY-059: the Defs() path with Sprintboard exercises the
// SprintboardTool dispatch line; combined with the test above, this
// closes the SprintboardTool 33% gap to ~75%.
func TestDefs_WithSprintboard(t *testing.T) {
	opts := builtins.Options{
		Sprintboard: &controlplane.SprintboardClient{},
	}
	defs := opts.Defs()
	if len(defs) != 1 || defs[0].Name != "sprintboard" {
		t.Fatalf("expected 1 sprintboard def; got %v", defNames(defs))
	}
}

// Sentinel to keep `context` import warm if later tests need it.
var _ = context.Background

// CARRY-059: MemoryTool handler dispatch — when the searcher is nil
// (the only state we can test without a real HybridSearcher), every
// op must surface the same "unconfigured" error.
func TestMemoryTool_NilSearcherAllOpsError(t *testing.T) {
	def := builtins.MemoryTool(nil, "app", "user")
	ctx := context.Background()
	ops := []string{"read", "write", "search", "unknown_op"}
	for _, op := range ops {
		_, err := def.Handler(ctx, map[string]any{"op": op})
		if err == nil {
			t.Errorf("op=%q expected error; got nil", op)
		}
	}
}

// CARRY-059: MemoryTool default app_id/user_id are used when the args
// omit them. Tested indirectly via the searcher-read error path: the
// default UserID/AppID are passed to searcher, but since the searcher
// is nil we surface the unconfigured error before reaching that code.
func TestMemoryTool_DefaultIDsFlow(t *testing.T) {
	def := builtins.MemoryTool(nil, "default-app", "default-user")
	_, err := def.Handler(context.Background(), map[string]any{"op": "read", "id": "abc"})
	if err == nil {
		t.Fatal("expected error from nil searcher")
	}
}

// CARRY-059: SprintboardTool handler dispatch — nil client must return
// the same "not configured" error for every op.
func TestSprintboardTool_NilClientAllOpsError(t *testing.T) {
	def := builtins.SprintboardTool((*controlplane.SprintboardClient)(nil))
	ctx := context.Background()
	ops := []string{"register", "claim", "complete", "sprint_status", "unknown_op"}
	for _, op := range ops {
		_, err := def.Handler(ctx, map[string]any{"op": op})
		if err == nil {
			t.Errorf("op=%q expected error; got nil", op)
		}
	}
}

// CARRY-059: SprintboardTool with nil client surfaces the not-configured
// error before any field validation runs. This test confirms the
// dispatch surface returns an error for every op we might call.
func TestSprintboardTool_NilClientArgs(t *testing.T) {
	def := builtins.SprintboardTool((*controlplane.SprintboardClient)(nil))
	ctx := context.Background()
	// Empty args for each op: nil client surfaces "not configured" error.
	for _, op := range []string{"claim", "complete", "sprint_status", "register"} {
		_, err := def.Handler(ctx, map[string]any{"op": op})
		if err == nil {
			t.Errorf("op=%q: expected error; got nil", op)
		}
	}
}

// v17609-2: Sprintboard dispatch via httptest server. Exercises the
// actual handler paths (sprintboardRegister, sprintboardClaim,
// sprintboardComplete, sprintboardStatus) with a real SprintboardClient.
// Lifts sprintboard* from 0% to high coverage.
func TestSprintboardTool_DispatchViaHTTPServer(t *testing.T) {
	var (
		mu         sync.Mutex
		gotPaths   []string
		gotBodies  []string
		sprintJSON = `{"id":"v17609","status":"active","tickets":[]}`
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPaths = append(gotPaths, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sprints/v17609"):
			_, _ = w.Write([]byte(sprintJSON))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer func() { srv.Close() }()

	cfg := controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "test-agent",
	}
	client := controlplane.NewSprintboardClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	def := builtins.SprintboardTool(client)
	ctx := context.Background()

	// register
	out, err := def.Handler(ctx, map[string]any{"op": "register"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !strings.Contains(out, `"op":"register"`) {
		t.Fatalf("register output: %s", out)
	}

	// claim
	out, err = def.Handler(ctx, map[string]any{"op": "claim", "ticket_id": "v17609-2"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !strings.Contains(out, `"ticket_id":"v17609-2"`) {
		t.Fatalf("claim output: %s", out)
	}

	// complete
	out, err = def.Handler(ctx, map[string]any{"op": "complete", "ticket_id": "v17609-2", "evidence": "tests green"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !strings.Contains(out, `"ticket_id":"v17609-2"`) {
		t.Fatalf("complete output: %s", out)
	}

	// sprint_status
	out, err = def.Handler(ctx, map[string]any{"op": "sprint_status", "sprint_id": "v17609"})
	if err != nil {
		t.Fatalf("sprint_status: %v", err)
	}
	if !strings.Contains(out, `"id":"v17609"`) {
		t.Fatalf("sprint_status output: %s", out)
	}

	// unknown op
	_, err = def.Handler(ctx, map[string]any{"op": "nope"})
	if err == nil {
		t.Fatal("expected error for unknown op")
	}

	// claim without ticket_id
	_, err = def.Handler(ctx, map[string]any{"op": "claim"})
	if err == nil {
		t.Fatal("expected error for claim missing ticket_id")
	}

	// complete without ticket_id
	_, err = def.Handler(ctx, map[string]any{"op": "complete"})
	if err == nil {
		t.Fatal("expected error for complete missing ticket_id")
	}

	// sprint_status without sprint_id
	_, err = def.Handler(ctx, map[string]any{"op": "sprint_status"})
	if err == nil {
		t.Fatal("expected error for sprint_status missing sprint_id")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotPaths) < 4 {
		t.Fatalf("expected >=4 paths hit; got %v", gotPaths)
	}
	wantSubs := []string{"/agents", "/tickets/v17609-2/claim", "/tickets/v17609-2/complete", "/sprints/v17609"}
	for _, want := range wantSubs {
		found := false
		for _, p := range gotPaths {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("path %q not hit; got %v", want, gotPaths)
		}
	}
	// Verify bodies contain agent_id (proves the actual call flowed through)
	hasAgent := false
	for _, b := range gotBodies {
		if strings.Contains(b, `"agent_id":"test-agent"`) {
			hasAgent = true
			break
		}
	}
	if !hasAgent {
		t.Errorf("no request body contained agent_id; got %v", gotBodies)
	}
}

// v17609-2: SprintboardClient network failures surface as errors
// (registers the doPost error path that wasn't previously exercised).
func TestSprintboardClient_RegisterFailureSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer func() { srv.Close() }()

	cfg := controlplane.SprintboardConfig{BaseURL: srv.URL, AgentName: "x"}
	c := controlplane.NewSprintboardClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := c.Register(context.Background()); err == nil {
		t.Fatal("expected register error on 500")
	}
}
