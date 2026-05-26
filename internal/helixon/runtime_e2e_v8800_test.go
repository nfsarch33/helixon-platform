package helixon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/helixon/channel"

	_ "modernc.org/sqlite"
)

// TestRuntime_MCPStdio_E2E wires the MCP stdio channel into a live Runtime
// and proves a tools/list + tools/call round-trip succeeds end-to-end:
// Runtime startup -> channel.Serve loop -> mcp.HandleRequest dispatch ->
// registered tool handler -> NDJSON response back on the wire.
func TestRuntime_MCPStdio_E2E(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(&stubProvider{resp: "ok"}, RuntimeConfig{
		AgentID:    "v8800-e2e",
		SessionDSN: "file::memory:?cache=shared",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, rt.Init(ctx))

	mcp := channel.NewMCPChannel(nil)
	require.NoError(t, mcp.RegisterTool(channel.MCPToolDef{
		Name:        "echo",
		Description: "Echo the text back",
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(params, &args)
			return args.Text, nil
		},
	}))

	pr, pw := io.Pipe()
	out := &lockedBuf{}

	stdio, err := NewMCPStdioChannel(mcp, MCPStdioChannelConfig{In: pr, Out: out})
	require.NoError(t, err)

	require.NoError(t, rt.Configure(ctx, WithChannel(stdio)))
	require.Equal(t, PhaseConfigured, rt.Phase())

	runErr := make(chan error, 1)
	go func() { runErr <- rt.Run(ctx) }()

	// Wait until the runtime reports Running so the stdio channel goroutine is live.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt.Phase() == PhaseRunning {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.Equal(t, PhaseRunning, rt.Phase())

	// Drive tools/list + tools/call through the pipe.
	listLine, _ := json.Marshal(channel.MCPRequest{JSONRPC: "2.0", Method: "tools/list", ID: 1})
	callParams, _ := json.Marshal(map[string]any{
		"name":      "echo",
		"arguments": map[string]string{"text": "v8800-e2e"},
	})
	callLine, _ := json.Marshal(channel.MCPRequest{JSONRPC: "2.0", Method: "tools/call", ID: 2, Params: callParams})

	go func() {
		defer pw.Close()
		_, _ = pw.Write(append(listLine, '\n'))
		_, _ = pw.Write(append(callLine, '\n'))
	}()

	// Wait for the second response to land.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), `"id":2`) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Contains(t, out.String(), `"id":1`, "expected tools/list response")
	require.Contains(t, out.String(), `"id":2`, "expected tools/call response")
	require.Contains(t, out.String(), "v8800-e2e", "echoed text must round-trip")

	require.NoError(t, rt.Shutdown(context.Background()))
	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}

// TestRuntime_HTTPLifecycle_E2E drives the runtime with a real HTTP channel
// and confirms phase transitions all the way through to a clean Shutdown.
// This is the smoke test for a Helixon container that only speaks HTTP.
func TestRuntime_HTTPLifecycle_E2E(t *testing.T) {
	t.Parallel()

	rt := NewRuntime(&stubProvider{resp: "ack"}, RuntimeConfig{
		AgentID:    "v8800-http",
		SessionDSN: "file::memory:?cache=shared",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, rt.Init(ctx))
	httpCh := NewHTTPChannel(HTTPChannelConfig{Addr: "127.0.0.1:0"})
	require.NoError(t, rt.Configure(ctx, WithChannel(httpCh)))

	runErr := make(chan error, 1)
	go func() { runErr <- rt.Run(ctx) }()

	// Wait for Running phase.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt.Phase() == PhaseRunning {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	assert.Equal(t, PhaseRunning, rt.Phase())

	require.NoError(t, rt.Shutdown(context.Background()))
	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
	assert.Equal(t, PhaseShutdown, rt.Phase())
}

// lockedBuf is a goroutine-safe bytes.Buffer for the stdio E2E test so
// the channel writer goroutine and the assertion goroutine never race.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
