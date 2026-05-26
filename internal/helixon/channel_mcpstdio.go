package helixon

import (
	"context"
	"io"

	"github.com/nfsarch33/helixon-platform/internal/helixon/channel"
)

// MCPStdioChannelConfig configures the helixon-side MCP stdio channel.
// It is a thin wrapper around channel.MCPStdioConfig; helixon owns the
// transport lifecycle (start/shutdown) while the MCP tool handlers live
// on the underlying *channel.MCPChannel.
type MCPStdioChannelConfig = channel.MCPStdioConfig

// NewMCPStdioChannel returns a Channel that exposes mcp over stdio. Each
// line on cfg.In must be a JSON-encoded MCP request; each response is
// emitted as one line to cfg.Out. The runtime's MessageHandler is unused
// because MCP tool dispatch goes through the registered tool handlers on
// mcp itself; passing it preserves the Channel.Serve signature.
func NewMCPStdioChannel(mcp *channel.MCPChannel, cfg MCPStdioChannelConfig) (Channel, error) {
	adapter, err := channel.NewMCPStdioAdapter(mcp, cfg)
	if err != nil {
		return nil, err
	}
	return mcpStdioChannel{adapter: adapter}, nil
}

// mcpStdioChannel adapts *channel.MCPStdioAdapter to helixon.Channel.
// The wrapper exists because channel.MCPStdioAdapter.Serve takes a typed
// handler from the channel package (to avoid an import cycle), and we
// want to satisfy helixon.Channel's MessageHandler shape transparently.
type mcpStdioChannel struct {
	adapter *channel.MCPStdioAdapter
}

func (c mcpStdioChannel) Name() string { return c.adapter.Name() }

func (c mcpStdioChannel) Serve(ctx context.Context, _ MessageHandler) error {
	// MCP dispatch is handler-driven on the *channel.MCPChannel, so the
	// runtime-level MessageHandler is intentionally unused here.
	return c.adapter.Serve(ctx, nil)
}

func (c mcpStdioChannel) Shutdown(ctx context.Context) error {
	return c.adapter.Shutdown(ctx)
}

// Compile-time guard that the wrapper implements the Channel interface.
var _ Channel = mcpStdioChannel{}

// MCPStdioFromStreams is a small convenience constructor for the common
// case of binding to in-memory or os.Stdin/os.Stdout streams.
func MCPStdioFromStreams(mcp *channel.MCPChannel, in io.Reader, out io.Writer) (Channel, error) {
	return NewMCPStdioChannel(mcp, MCPStdioChannelConfig{In: in, Out: out})
}
