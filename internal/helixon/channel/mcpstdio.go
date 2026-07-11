package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// MCPStdioConfig configures the stdio adapter that exposes an [MCPChannel]
// as a long-running JSON-RPC line server over an io.Reader / io.Writer
// pair. It is used to plug Helixon into an MCP host (Claude Desktop,
// sprintboard-mcp's parent process) without binding to a TCP port.
type MCPStdioConfig struct {
	// In is the JSON-RPC request stream (one MCPRequest per line).
	In io.Reader
	// Out is the JSON-RPC response sink (one MCPResponse per line).
	// Writes are serialised by the adapter so multiple in-flight calls
	// cannot interleave bytes.
	Out io.Writer
	// MaxLineBytes bounds bufio.Scanner so a single oversized request
	// cannot blow the read buffer. Default 4 MiB.
	MaxLineBytes int
	// Logger receives structured warnings for malformed input and
	// transport errors. Defaults to slog.Default.
	Logger *slog.Logger
}

func (c MCPStdioConfig) withDefaults() MCPStdioConfig {
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = 4 * 1024 * 1024
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// MCPStdioAdapter implements [Channel] (via the helixon package) by binding
// an [MCPChannel] to a stdio JSON-RPC stream. Each line of In is decoded
// into an MCPRequest, dispatched to MCPChannel.HandleRequest, and the
// MCPResponse is encoded back as a single line to Out. The adapter is
// safe to call from a single goroutine; concurrent writes to Out are
// serialised.
type MCPStdioAdapter struct {
	mcp     *MCPChannel
	cfg     MCPStdioConfig
	logger  *slog.Logger
	writeMu sync.Mutex
	closeMu sync.Mutex
	closed  bool
}

// NewMCPStdioAdapter wraps an MCPChannel with the stdio transport.
func NewMCPStdioAdapter(mcp *MCPChannel, cfg MCPStdioConfig) (*MCPStdioAdapter, error) {
	if mcp == nil {
		return nil, errors.New("mcp stdio: MCPChannel is required")
	}
	if cfg.In == nil {
		return nil, errors.New("mcp stdio: In reader is required")
	}
	if cfg.Out == nil {
		return nil, errors.New("mcp stdio: Out writer is required")
	}
	cfg = cfg.withDefaults()
	return &MCPStdioAdapter{
		mcp:    mcp,
		cfg:    cfg,
		logger: cfg.Logger.With(slog.String("component", "helixon.channel.mcpstdio")),
	}, nil
}

// Name implements helixon.Channel.
func (a *MCPStdioAdapter) Name() string { return "mcp-stdio" }

// Serve reads requests from In one line at a time, dispatches them to the
// underlying MCPChannel, and writes responses to Out. Serve blocks until
// In returns EOF, ctx is cancelled, or a write error occurs. It returns
// nil on graceful EOF / context cancellation.
//
// The handler argument from helixon.Channel is unused here: MCP tool
// dispatch goes through the registered MCPToolHandler set on each
// MCPToolDef rather than the channel-level MessageHandler.
func (a *MCPStdioAdapter) Serve(ctx context.Context, _ helixonHandler) error {
	scanner := bufio.NewScanner(a.cfg.In)
	scanner.Buffer(make([]byte, 0, 64*1024), a.cfg.MaxLineBytes)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req MCPRequest
		if err := json.Unmarshal(line, &req); err != nil {
			a.logger.Warn("malformed JSON-RPC line, sending parse error",
				slog.String("error", err.Error()))
			if werr := a.writeResponse(MCPResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &MCPError{Code: -32700, Message: fmt.Sprintf("parse error: %s", err.Error())},
			}); werr != nil {
				return werr
			}
			continue
		}
		resp := a.mcp.HandleRequest(ctx, req)
		if err := a.writeResponse(resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp stdio scanner: %w", err)
	}
	return nil
}

// Shutdown marks the adapter closed. The underlying io.Reader is the
// caller's responsibility (typically os.Stdin); the adapter only ensures
// any in-flight writes finish. If Out is an io.Closer, it is closed.
func (a *MCPStdioAdapter) Shutdown(_ context.Context) error {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if c, ok := a.cfg.Out.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (a *MCPStdioAdapter) writeResponse(resp MCPResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("mcp stdio marshal: %w", err)
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if _, err := a.cfg.Out.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("mcp stdio write: %w", err)
	}
	return nil
}

// helixonHandler aliases the channel-level message handler so this
// package does not import helixon (which would create an import cycle).
// The signature matches helixon.MessageHandler exactly; callers in the
// helixon package can pass their MessageHandler unchanged.
type helixonHandler = func(ctx context.Context, msg IncomingMessage) (string, error)

// IncomingMessage mirrors helixon.IncomingMessage so the channel package
// can build adapter signatures without an import cycle. Helixon callers
// should treat the two structs as interchangeable.
type IncomingMessage struct {
	SessionID string `json:"session_id,omitempty"`
	Channel   string `json:"channel"`
	Content   string `json:"content"`
}
