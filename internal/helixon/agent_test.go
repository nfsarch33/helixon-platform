package helixon

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

	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"

	_ "modernc.org/sqlite"
)

func TestNewRuntime(t *testing.T) {
	rt := NewRuntime(nil, RuntimeConfig{
		AgentID:      "test-agent",
		SystemPrompt: "You are a test agent.",
	})

	if rt.Phase() != PhaseCreated {
		t.Errorf("expected phase Created, got %s", rt.Phase())
	}
	if rt.cfg.AgentID != "test-agent" {
		t.Errorf("expected agent_id test-agent, got %s", rt.cfg.AgentID)
	}
}

func TestRuntimeConfigDefaults(t *testing.T) {
	cfg := RuntimeConfig{}.withDefaults()

	if cfg.AgentID != "helixon-default" {
		t.Errorf("default AgentID = %q, want helixon-default", cfg.AgentID)
	}
	if cfg.MaxIterations != 25 {
		t.Errorf("default MaxIterations = %d, want 25", cfg.MaxIterations)
	}
	if cfg.MaxTokens != 128000 {
		t.Errorf("default MaxTokens = %d, want 128000", cfg.MaxTokens)
	}
	if cfg.Timeout != 5*time.Minute {
		t.Errorf("default Timeout = %v, want 5m", cfg.Timeout)
	}
	if cfg.HeartbeatEvery != 60*time.Second {
		t.Errorf("default HeartbeatEvery = %v, want 60s", cfg.HeartbeatEvery)
	}
	if cfg.Logger == nil {
		t.Error("default Logger should not be nil")
	}
}

func TestPhaseTransitionErrors(t *testing.T) {
	rt := NewRuntime(nil, RuntimeConfig{})

	// Configure should fail before Init
	if err := rt.Configure(context.Background()); err == nil {
		t.Error("Configure before Init should fail")
	}

	// Run should fail before Configure
	if err := rt.Run(context.Background()); err == nil {
		t.Error("Run before Configure should fail")
	}

	// Shutdown should fail before Run
	if err := rt.Shutdown(context.Background()); err == nil {
		t.Error("Shutdown before Run should fail")
	}
}

// TODO: Requires SQLite; run when Go module resolution works.
// func TestRuntimeInit(t *testing.T) {
//     provider := &mockProvider{}
//     rt := NewRuntime(provider, RuntimeConfig{
//         SessionDSN: "file::memory:?cache=shared",
//     })
//     ctx := context.Background()
//     if err := rt.Init(ctx); err != nil {
//         t.Fatalf("Init failed: %v", err)
//     }
//     if rt.Phase() != PhaseInit {
//         t.Errorf("expected phase Init, got %s", rt.Phase())
//     }
//     if rt.Registry() == nil {
//         t.Error("registry should not be nil after Init")
//     }
// }

// TODO: Full lifecycle test; requires provider mock and SQLite.
// func TestRuntimeFullLifecycle(t *testing.T) {
//     provider := &mockProvider{}
//     rt := NewRuntime(provider, RuntimeConfig{
//         SessionDSN: "file::memory:?cache=shared",
//     })
//     ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//     defer cancel()
//
//     if err := rt.Init(ctx); err != nil {
//         t.Fatalf("Init: %v", err)
//     }
//     ch := NewHTTPChannel(HTTPChannelConfig{Addr: ":0"})
//     if err := rt.Configure(ctx, WithChannel(ch)); err != nil {
//         t.Fatalf("Configure: %v", err)
//     }
//
//     errCh := make(chan error, 1)
//     go func() { errCh <- rt.Run(ctx) }()
//     time.Sleep(100 * time.Millisecond)
//
//     if err := rt.Shutdown(ctx); err != nil {
//         t.Fatalf("Shutdown: %v", err)
//     }
// }

func TestChannelInterface(t *testing.T) {
	http := NewHTTPChannel(HTTPChannelConfig{})
	if http.Name() != "http" {
		t.Errorf("HTTPChannel.Name() = %q, want http", http.Name())
	}

	ws := NewWebSocketChannel(WebSocketChannelConfig{})
	if ws.Name() != "websocket" {
		t.Errorf("WebSocketChannel.Name() = %q, want websocket", ws.Name())
	}

	cli := NewCLIChannel(func(_ context.Context, _ MessageHandler) error { return nil })
	if cli.Name() != "cli" {
		t.Errorf("CLIChannel.Name() = %q, want cli", cli.Name())
	}
}

func TestMultiplexChannels(t *testing.T) {
	channels := []Channel{
		NewHTTPChannel(HTTPChannelConfig{}),
		NewWebSocketChannel(WebSocketChannelConfig{}),
	}
	if err := multiplexChannels(channels); err != nil {
		t.Errorf("multiplexChannels with unique names should not error: %v", err)
	}

	dupChannels := []Channel{
		NewHTTPChannel(HTTPChannelConfig{}),
		NewHTTPChannel(HTTPChannelConfig{}),
	}
	if err := multiplexChannels(dupChannels); err == nil {
		t.Error("multiplexChannels with duplicate names should error")
	}
}

func TestDescribeChannels(t *testing.T) {
	channels := []Channel{
		NewHTTPChannel(HTTPChannelConfig{}),
		NewWebSocketChannel(WebSocketChannelConfig{}),
	}
	infos := DescribeChannels(channels)

	if len(infos) != 2 {
		t.Fatalf("expected 2 channel infos, got %d", len(infos))
	}
	if infos[0].Name != "http" {
		t.Errorf("first channel name = %q, want http", infos[0].Name)
	}
	if infos[1].Name != "websocket" {
		t.Errorf("second channel name = %q, want websocket", infos[1].Name)
	}
}

func TestHTTPChannelConfigDefaults(t *testing.T) {
	cfg := HTTPChannelConfig{}.withDefaults()
	if cfg.Addr != ":8686" {
		t.Errorf("default addr = %q, want :8686", cfg.Addr)
	}
	if cfg.ReadTimeout != 30*time.Second {
		t.Errorf("default ReadTimeout = %v, want 30s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 120*time.Second {
		t.Errorf("default WriteTimeout = %v, want 120s", cfg.WriteTimeout)
	}
}

func TestWebSocketChannelConfigDefaults(t *testing.T) {
	cfg := WebSocketChannelConfig{}.withDefaults()
	if cfg.Addr != ":8687" {
		t.Errorf("default addr = %q, want :8687", cfg.Addr)
	}
}

// TestWithAgentrace_RecordsToolCalls confirms WithAgentrace wraps the runtime's
// tool executor so every Execute writes one NDJSON line to the configured
// log path. This is the regression guard for the canonical-vs-secondary
// asymmetry: a sink failure must never block tool dispatch, but a green
// path must always emit one event per call.
func TestWithAgentrace_RecordsToolCalls(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "v8000-agentrace.ndjson")

	rt := NewRuntime(nil, RuntimeConfig{
		AgentID:    "claude-code-test",
		SessionDSN: "file::memory:?cache=shared",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := rt.Configure(ctx, WithAgentrace(tooldispatch.AgentraceConfig{
		LogPath: logPath, Server: "helixon-test",
	})); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() {
		if rt.traced != nil {
			_ = rt.traced.Close()
		}
		if rt.store != nil {
			_ = rt.store.Close()
		}
	})

	if err := rt.registry.Register(tooldispatch.ToolDef{
		Name:        "echo",
		Description: "echo the message",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			s, _ := args["msg"].(string)
			return s, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Execute via the wrapped executor so the trace fires.
	got, err := rt.executor.Execute(ctx, "echo", `{"msg":"v8000-overnight"}`)
	if err != nil {
		t.Fatalf("executor.Execute: %v", err)
	}
	if got != "v8000-overnight" {
		t.Fatalf("Execute result = %q, want v8000-overnight", got)
	}

	// Force the sink to flush before reading.
	if err := rt.traced.Close(); err != nil {
		t.Fatalf("traced.Close: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read agentrace log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("agentrace lines = %d, want 1: %q", len(lines), string(data))
	}
	var ev struct {
		Tool    string `json:"tool"`
		Server  string `json:"server"`
		AgentID string `json:"agent_id"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if ev.Tool != "echo" || ev.Server != "helixon-test" || ev.AgentID != "claude-code-test" || !ev.Success {
		t.Fatalf("event mismatch: %+v", ev)
	}
}

// TestWebSocketChannel_Scaffold_Returns501 locks the current /ws contract:
// until gorilla/websocket (or an equivalent upgrade) is wired, the endpoint
// must respond HTTP 501 with a documented error body. This guards against
// silent regressions if a future change accidentally swaps the handler before
// the upgrade path is fully implemented and exercised end-to-end.
func TestWebSocketChannel_Scaffold_Returns501(t *testing.T) {
	t.Parallel()

	ws := NewWebSocketChannel(WebSocketChannelConfig{})

	srv := httptest.NewServer(ws.scaffoldHandler())
	defer func() { srv.Close() }()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /ws scaffold: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(body["error"], "WebSocket upgrade not yet implemented") {
		t.Errorf("error body = %q, want it to mention 'WebSocket upgrade not yet implemented'", body["error"])
	}
	if !strings.Contains(body["error"], "gorilla/websocket") {
		t.Errorf("error body = %q, want it to reference gorilla/websocket so the gap is discoverable", body["error"])
	}
}
