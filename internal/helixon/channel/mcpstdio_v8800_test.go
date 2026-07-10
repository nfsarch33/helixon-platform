package channel

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
)

// safeBuf is a minimal goroutine-safe Write/Read buffer used by the stdio
// tests so the adapter goroutine and the assertion goroutine never race.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

//nolint:unparam // second return (channel) is kept for future tests that need both adapter and channel handles.
func newAdapter(t *testing.T, in io.Reader, out io.Writer) (*MCPStdioAdapter, *MCPChannel) {
	t.Helper()
	mcp := NewMCPChannel(nil)
	require.NoError(t, mcp.RegisterTool(MCPToolDef{
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
	a, err := NewMCPStdioAdapter(mcp, MCPStdioConfig{In: in, Out: out})
	require.NoError(t, err)
	return a, mcp
}

func TestMCPStdioAdapter_RoundTripInitializeAndToolsCall(t *testing.T) {
	t.Parallel()

	initLine, _ := json.Marshal(MCPRequest{JSONRPC: "2.0", Method: "initialize", ID: 1})
	listLine, _ := json.Marshal(MCPRequest{JSONRPC: "2.0", Method: "tools/list", ID: 2})
	callParams, _ := json.Marshal(map[string]any{
		"name":      "echo",
		"arguments": map[string]string{"text": "v8800-stdio"},
	})
	callLine, _ := json.Marshal(MCPRequest{JSONRPC: "2.0", Method: "tools/call", ID: 3, Params: callParams})

	in := strings.NewReader(string(initLine) + "\n" + string(listLine) + "\n" + string(callLine) + "\n")
	out := &safeBuf{}

	a, _ := newAdapter(t, in, out)

	require.NoError(t, a.Serve(context.Background(), nil))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 3, "expected exactly 3 response lines")

	var initResp, listResp, callResp MCPResponse
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &initResp))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &listResp))
	require.NoError(t, json.Unmarshal([]byte(lines[2]), &callResp))

	assert.Equal(t, float64(1), initResp.ID)
	assert.Nil(t, initResp.Error)
	assert.Equal(t, float64(2), listResp.ID)
	assert.Nil(t, listResp.Error)
	assert.Equal(t, float64(3), callResp.ID)
	require.Nil(t, callResp.Error)

	// tools/call result should embed the echoed text in the content array.
	rawCall, _ := json.Marshal(callResp.Result)
	assert.Contains(t, string(rawCall), "v8800-stdio")
}

func TestMCPStdioAdapter_MalformedLineProducesParseError(t *testing.T) {
	t.Parallel()

	in := strings.NewReader("not-json\n")
	out := &safeBuf{}
	a, _ := newAdapter(t, in, out)

	require.NoError(t, a.Serve(context.Background(), nil))

	var resp MCPResponse
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32700, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "parse error")
}

func TestMCPStdioAdapter_BlankLinesIgnored(t *testing.T) {
	t.Parallel()

	in := strings.NewReader("\n\n")
	out := &safeBuf{}
	a, _ := newAdapter(t, in, out)
	require.NoError(t, a.Serve(context.Background(), nil))
	assert.Empty(t, strings.TrimSpace(out.String()), "blank lines must produce no responses")
}

func TestMCPStdioAdapter_RejectsNilDeps(t *testing.T) {
	t.Parallel()

	_, err := NewMCPStdioAdapter(nil, MCPStdioConfig{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	require.Error(t, err)
	_, err = NewMCPStdioAdapter(NewMCPChannel(nil), MCPStdioConfig{In: nil, Out: &bytes.Buffer{}})
	require.Error(t, err)
	_, err = NewMCPStdioAdapter(NewMCPChannel(nil), MCPStdioConfig{In: strings.NewReader(""), Out: nil})
	require.Error(t, err)
}

// TestMCPStdioAdapter_ContextCancelStopsServe pumps requests through a pipe
// and verifies that cancelling the context lets Serve return without
// blocking on the still-open pipe reader.
func TestMCPStdioAdapter_ContextCancelStopsServe(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()

	out := &safeBuf{}
	a, _ := newAdapter(t, pr, out)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, nil) }()

	// Send one valid request so the scanner advances at least once.
	line, _ := json.Marshal(MCPRequest{JSONRPC: "2.0", Method: "initialize", ID: 7})
	_, _ = pw.Write(append(line, '\n'))

	// Wait for the response to appear so we know the loop reached the
	// ctx-check inside the next iteration.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), `"id":7`) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Contains(t, out.String(), `"id":7`, "expected response before cancel")

	cancel()
	_ = pw.Close()

	select {
	case err := <-done:
		require.NoError(t, err, "Serve must return cleanly on cancel/EOF")
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestMCPStdioAdapter_NameAndShutdown(t *testing.T) {
	t.Parallel()
	out := &safeBuf{}
	a, _ := newAdapter(t, strings.NewReader(""), out)
	assert.Equal(t, "mcp-stdio", a.Name())
	require.NoError(t, a.Shutdown(context.Background()))
	// Idempotent.
	require.NoError(t, a.Shutdown(context.Background()))
}
