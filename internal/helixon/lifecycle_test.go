package helixon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

type stubProvider struct{ resp string }

func (s *stubProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: s.resp}}},
		Usage:   llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

// fakeChannel is a Channel that signals readiness, blocks until ctx.Done(),
// and records its lifecycle for assertions.
type fakeChannel struct {
	name      string
	served    chan struct{}
	shutdowns int32
}

func newFakeChannel(name string) *fakeChannel {
	return &fakeChannel{name: name, served: make(chan struct{}, 1)}
}

func (f *fakeChannel) Name() string { return f.name }

func (f *fakeChannel) Serve(ctx context.Context, _ MessageHandler) error {
	select {
	case f.served <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil
}

func (f *fakeChannel) Shutdown(_ context.Context) error {
	atomic.AddInt32(&f.shutdowns, 1)
	return nil
}

func (f *fakeChannel) ShutdownCount() int { return int(atomic.LoadInt32(&f.shutdowns)) }

// TestRuntime_FullLifecycle drives Created -> Init -> Configured -> Running ->
// Shutdown with a real session store and a fake channel, exercising the
// happy-path branches that none of the existing tests reach.
func TestRuntime_FullLifecycle(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(&stubProvider{resp: "ok"}, RuntimeConfig{
		AgentID:        "lifecycle-test",
		SessionDSN:     "file::memory:?cache=shared",
		HeartbeatEvery: 25 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if rt.Phase() != PhaseInit {
		t.Fatalf("phase after Init = %s, want init", rt.Phase())
	}
	if rt.Registry() == nil {
		t.Fatalf("Registry() nil after Init")
	}

	ch := newFakeChannel("fake")
	if err := rt.Configure(ctx, WithChannel(ch)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if rt.Phase() != PhaseConfigured {
		t.Fatalf("phase after Configure = %s, want configured", rt.Phase())
	}

	runErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr <- rt.Run(ctx)
	}()

	// Wait until the channel reports it's serving so the runtime is in Running.
	select {
	case <-ch.served:
	case <-time.After(time.Second):
		t.Fatal("fakeChannel never received Serve call")
	}

	if rt.Phase() != PhaseRunning {
		t.Fatalf("phase during Run = %s, want running", rt.Phase())
	}

	// Shutdown should transition Running -> Shutdown and stop the channel.
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if rt.Phase() != PhaseShutdown {
		t.Fatalf("phase after Shutdown = %s, want shutdown", rt.Phase())
	}

	wg.Wait()
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned err = %v, want nil", err)
	}
	if got := ch.ShutdownCount(); got != 1 {
		t.Fatalf("channel Shutdown called %d times, want 1", got)
	}
}

// TestRegisterBuiltinTools_NoSubsystems verifies the builtin registration
// is a no-op when neither memory nor sprintboard is wired (the minimum-viable
// runtime case). The function still returns nil and the registry stays empty
// of namespaced tools.
func TestRegisterBuiltinTools_NoSubsystems(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(&stubProvider{}, RuntimeConfig{
		AgentID:    "no-subsystems",
		SessionDSN: "file::memory:?cache=shared",
	})
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = rt.store.Close() })

	if err := RegisterBuiltinTools(rt); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}

	for _, name := range rt.registry.Names() {
		if strings.HasPrefix(name, "memory.") || strings.HasPrefix(name, "sprintboard.") {
			t.Fatalf("expected no namespaced tools without subsystems, got %q", name)
		}
	}
}

// TestRegisterBuiltinTools_RegistryNotInitialised guards the precondition.
func TestRegisterBuiltinTools_RegistryNotInitialised(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(&stubProvider{}, RuntimeConfig{AgentID: "x"})
	err := RegisterBuiltinTools(rt)
	if err == nil || !strings.Contains(err.Error(), "registry not initialised") {
		t.Fatalf("expected registry-not-initialised error, got %v", err)
	}
}

// TestRegisterBuiltinTools_WithSprintboard registers a SprintboardClient and
// invokes the resulting `sprintboard.claim_ticket` and
// `sprintboard.complete_ticket` tools end-to-end against an httptest server.
// This exercises both the registration code path AND the tool handler bodies.
func TestRegisterBuiltinTools_WithSprintboard(t *testing.T) {
	t.Parallel()

	var seen struct {
		sync.Mutex
		paths  []string
		bodies []string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		seen.Lock()
		seen.paths = append(seen.paths, r.URL.Path)
		seen.bodies = append(seen.bodies, buf.String())
		seen.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rt := NewRuntime(&stubProvider{}, RuntimeConfig{
		AgentID:    "claude-code-sb-test",
		SessionDSN: "file::memory:?cache=shared",
	})
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = rt.store.Close() })

	rt.sprintCtl = controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: rt.cfg.AgentID,
	}, nil)

	if err := RegisterBuiltinTools(rt); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}

	names := rt.registry.Names()
	if !contains(names, "sprintboard.claim_ticket") || !contains(names, "sprintboard.complete_ticket") {
		t.Fatalf("expected sprintboard.* tools registered, got %v", names)
	}

	got, err := rt.executor.Execute(ctx, "sprintboard.claim_ticket",
		`{"ticket_id":"T-8200-LCT"}`)
	if err != nil {
		t.Fatalf("claim_ticket: %v", err)
	}
	if !strings.Contains(got, "T-8200-LCT claimed by claude-code-sb-test") {
		t.Fatalf("claim_ticket result = %q", got)
	}

	if _, err := rt.executor.Execute(ctx, "sprintboard.complete_ticket",
		`{"ticket_id":"T-8200-LCT","evidence":"green"}`); err != nil {
		t.Fatalf("complete_ticket: %v", err)
	}

	// Validate handler argument validation: missing ticket_id -> error.
	if _, err := rt.executor.Execute(ctx, "sprintboard.claim_ticket", `{}`); err == nil {
		t.Fatal("expected error from claim_ticket with missing ticket_id")
	}

	seen.Lock()
	defer seen.Unlock()
	if len(seen.paths) != 2 {
		t.Fatalf("server saw %d requests, want 2: %v", len(seen.paths), seen.paths)
	}
	if !strings.Contains(seen.bodies[0], `"ticket_id":"T-8200-LCT"`) {
		t.Fatalf("first body = %q", seen.bodies[0])
	}
	if !strings.Contains(seen.bodies[1], `"evidence":"green"`) {
		t.Fatalf("second body = %q", seen.bodies[1])
	}
}

// TestNamespacedRegistry_RegisterListUnregister verifies the namespace
// prefixing, listing, and bulk-removal paths.
func TestNamespacedRegistry_RegisterListUnregister(t *testing.T) {
	t.Parallel()

	inner := tooldispatch.NewRegistry(nil)
	nr := NewNamespacedRegistry(inner, nil)

	register := func(ns, name string) {
		t.Helper()
		if err := nr.RegisterNamespaced(ns, tooldispatch.ToolDef{
			Name: name,
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				return ns + "." + name, nil
			},
		}); err != nil {
			t.Fatalf("RegisterNamespaced(%s,%s): %v", ns, name, err)
		}
	}

	register("memory", "search")
	register("memory", "write")
	register("sprintboard", "claim_ticket")
	register("", "raw_tool")

	wantNames := []string{"memory.search", "memory.write", "sprintboard.claim_ticket", "raw_tool"}
	for _, n := range wantNames {
		if !contains(inner.Names(), n) {
			t.Fatalf("missing registered name %q in %v", n, inner.Names())
		}
	}

	ns := nr.ListNamespaces()
	if !contains(ns, "memory") || !contains(ns, "sprintboard") {
		t.Fatalf("ListNamespaces() = %v, want memory + sprintboard", ns)
	}
	// Unprefixed tools should not surface as a namespace.
	for _, n := range ns {
		if n == "" || n == "raw_tool" {
			t.Fatalf("unprefixed tool leaked into ListNamespaces(): %v", ns)
		}
	}

	removed := nr.UnregisterNamespace("memory")
	if removed != 2 {
		t.Fatalf("UnregisterNamespace(memory) removed %d tools, want 2", removed)
	}

	left := inner.Names()
	if contains(left, "memory.search") || contains(left, "memory.write") {
		t.Fatalf("memory.* still present after UnregisterNamespace: %v", left)
	}
	if !contains(left, "sprintboard.claim_ticket") || !contains(left, "raw_tool") {
		t.Fatalf("non-memory tools should remain, got %v", left)
	}

	// UnregisterNamespace on a namespace with no tools should report 0 and not
	// emit a "tools_removed" log line (we just assert the count here).
	if got := nr.UnregisterNamespace("absent"); got != 0 {
		t.Fatalf("UnregisterNamespace(absent) = %d, want 0", got)
	}
}

// TestHTTPChannel_ChatAndHealth round-trips a chat request through the real
// HTTP channel handler and checks the /health endpoint, locking both
// happy-path and bad-request behaviour.
func TestHTTPChannel_ChatAndHealth(t *testing.T) {
	t.Parallel()

	ch := NewHTTPChannel(HTTPChannelConfig{})

	handler := func(_ context.Context, msg IncomingMessage) (string, error) {
		if msg.Channel != "http" {
			return "", fmt.Errorf("expected channel http, got %s", msg.Channel)
		}
		if msg.Content == "explode" {
			return "", errors.New("explosive failure")
		}
		return "echo:" + msg.Content, nil
	}

	chatSrv := httptest.NewServer(ch.chatHandler(handler))
	defer chatSrv.Close()
	healthSrv := httptest.NewServer(ch.healthHandler())
	defer healthSrv.Close()

	// Happy path.
	body, _ := json.Marshal(map[string]string{"message": "hello", "session_id": "s1"})
	resp, err := http.Post(chatSrv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Response != "echo:hello" {
		t.Fatalf("response = %q, want echo:hello", out.Response)
	}

	// Bad JSON -> 400.
	bad, err := http.Post(chatSrv.URL, "application/json", strings.NewReader("not-json"))
	if err != nil {
		t.Fatalf("POST bad: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400", bad.StatusCode)
	}

	// Empty message -> 400.
	empty, _ := json.Marshal(map[string]string{"message": ""})
	em, err := http.Post(chatSrv.URL, "application/json", bytes.NewReader(empty))
	if err != nil {
		t.Fatalf("POST empty: %v", err)
	}
	em.Body.Close()
	if em.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty message status = %d, want 400", em.StatusCode)
	}

	// Handler error -> 500.
	bang, _ := json.Marshal(map[string]string{"message": "explode"})
	br, err := http.Post(chatSrv.URL, "application/json", bytes.NewReader(bang))
	if err != nil {
		t.Fatalf("POST explode: %v", err)
	}
	defer br.Body.Close()
	if br.StatusCode != http.StatusInternalServerError {
		t.Fatalf("handler-error status = %d, want 500", br.StatusCode)
	}
	var berr chatResponse
	_ = json.NewDecoder(br.Body).Decode(&berr)
	if !strings.Contains(berr.Error, "explosive failure") {
		t.Fatalf("error body = %q", berr.Error)
	}

	// /health.
	hr, err := http.Get(healthSrv.URL)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer hr.Body.Close()
	if hr.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", hr.StatusCode)
	}
	var hb map[string]string
	_ = json.NewDecoder(hr.Body).Decode(&hb)
	if hb["status"] != "ok" || hb["channel"] != "http" {
		t.Fatalf("health body = %v, want status=ok channel=http", hb)
	}
}

// TestHTTPChannel_ShutdownIdempotent verifies Shutdown on a channel that was
// never served is a no-op (server is nil).
func TestHTTPChannel_ShutdownIdempotent(t *testing.T) {
	t.Parallel()
	ch := NewHTTPChannel(HTTPChannelConfig{})
	if err := ch.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on un-served channel = %v, want nil", err)
	}
	ws := NewWebSocketChannel(WebSocketChannelConfig{})
	if err := ws.Shutdown(context.Background()); err != nil {
		t.Fatalf("ws Shutdown on un-served channel = %v, want nil", err)
	}
}

// TestRuntime_DoubleShutdownReturnsError locks the precondition: Shutdown
// after Shutdown is a phase mismatch, not a crash.
func TestRuntime_DoubleShutdownReturnsError(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(&stubProvider{}, RuntimeConfig{
		AgentID:    "double-shutdown",
		SessionDSN: "file::memory:?cache=shared",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ch := newFakeChannel("dbl")
	if err := rt.Configure(ctx, WithChannel(ch)); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- rt.Run(ctx) }()
	<-ch.served

	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	<-runErr

	err := rt.Shutdown(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Shutdown requires phase Running") {
		t.Fatalf("second Shutdown returned %v, want phase-mismatch error", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
