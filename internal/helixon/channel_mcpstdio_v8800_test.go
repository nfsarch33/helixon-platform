package helixon

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/helixon/channel"
)

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMCPStdioChannel_ImplementsHelixonChannel(t *testing.T) {
	t.Parallel()

	mcp := channel.NewMCPChannel(nil)
	require.NoError(t, mcp.RegisterTool(channel.MCPToolDef{
		Name: "ping",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "pong", nil
		},
	}))

	in := strings.NewReader(mustLine(channel.MCPRequest{JSONRPC: "2.0", Method: "tools/list", ID: 1}))
	out := &syncBuf{}

	ch, err := NewMCPStdioChannel(mcp, MCPStdioChannelConfig{In: in, Out: out})
	require.NoError(t, err)
	assert.Equal(t, "mcp-stdio", ch.Name())

	require.NoError(t, ch.Serve(context.Background(), nil))

	var resp channel.MCPResponse
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp))
	assert.Nil(t, resp.Error)
	assert.Equal(t, float64(1), resp.ID)

	require.NoError(t, ch.Shutdown(context.Background()))
}

func TestMCPStdioChannel_RejectsNilMCPChannel(t *testing.T) {
	t.Parallel()

	_, err := NewMCPStdioChannel(nil, MCPStdioChannelConfig{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	require.Error(t, err)
}

func mustLine(req channel.MCPRequest) string {
	data, _ := json.Marshal(req)
	return string(data) + "\n"
}
