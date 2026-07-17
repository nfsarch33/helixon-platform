package helixon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// IncomingMessage is a channel-agnostic message arriving at the runtime.
type IncomingMessage struct {
	SessionID string `json:"session_id,omitempty"`
	Channel   string `json:"channel"`
	Content   string `json:"content"`
}

// MessageHandler processes an incoming message and returns a response.
type MessageHandler func(ctx context.Context, msg IncomingMessage) (string, error)

// Channel is the transport abstraction for the Helixon runtime.
// Implementations handle transport-specific concerns (HTTP, WebSocket, CLI)
// and delegate message processing to the MessageHandler provided by the runtime.
type Channel interface {
	Name() string
	Serve(ctx context.Context, handler MessageHandler) error
	Shutdown(ctx context.Context) error
}

// HTTPChannelConfig configures the HTTP REST channel.
type HTTPChannelConfig struct {
	Addr         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	Logger       *slog.Logger
}

func (c HTTPChannelConfig) withDefaults() HTTPChannelConfig {
	if c.Addr == "" {
		c.Addr = ":8686"
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = 30 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 120 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// HTTPChannel serves agent interactions over a REST API.
type HTTPChannel struct {
	cfg    HTTPChannelConfig
	server *http.Server
	logger *slog.Logger
}

// NewHTTPChannel creates an HTTP channel for REST-based agent interaction.
func NewHTTPChannel(cfg HTTPChannelConfig) *HTTPChannel {
	cfg = cfg.withDefaults()
	return &HTTPChannel{
		cfg:    cfg,
		logger: cfg.Logger.With(slog.String("component", "helixon.channel.http")),
	}
}

func (h *HTTPChannel) Name() string { return "http" }

func (h *HTTPChannel) Serve(ctx context.Context, handler MessageHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/chat", h.chatHandler(handler))
	mux.HandleFunc("GET /api/v1/health", h.healthHandler())

	h.server = &http.Server{
		Addr:         h.cfg.Addr,
		Handler:      mux,
		ReadTimeout:  h.cfg.ReadTimeout,
		WriteTimeout: h.cfg.WriteTimeout,
	}

	h.logger.Info("HTTP channel listening", slog.String("addr", h.cfg.Addr))
	return runServerUntilCancel(ctx, h.server)
}

func (h *HTTPChannel) Shutdown(ctx context.Context) error {
	if h.server == nil {
		return nil
	}
	return h.server.Shutdown(ctx)
}

type chatRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

type chatResponse struct {
	SessionID string `json:"session_id,omitempty"`
	Response  string `json:"response"`
	Error     string `json:"error,omitempty"`
}

func (h *HTTPChannel) chatHandler(handler MessageHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, chatResponse{Error: "invalid request body"})
			return
		}
		if req.Message == "" {
			writeJSON(w, http.StatusBadRequest, chatResponse{Error: "message is required"})
			return
		}

		msg := IncomingMessage{
			SessionID: req.SessionID,
			Channel:   "http",
			Content:   req.Message,
		}

		response, err := handler(r.Context(), msg)
		if err != nil {
			h.logger.Warn("chat error", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, chatResponse{Error: err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, chatResponse{
			SessionID: msg.SessionID,
			Response:  response,
		})
	}
}

func (h *HTTPChannel) healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"channel": "http",
		})
	}
}

// WebSocketChannelConfig configures the WebSocket channel.
type WebSocketChannelConfig struct {
	Addr   string
	Logger *slog.Logger
}

func (c WebSocketChannelConfig) withDefaults() WebSocketChannelConfig {
	if c.Addr == "" {
		c.Addr = ":8687"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// WebSocketChannel serves agent interactions over WebSocket for streaming.
// The implementation uses gorilla/websocket (deferred until Go deps resolve).
type WebSocketChannel struct {
	cfg    WebSocketChannelConfig
	server *http.Server
	logger *slog.Logger
}

// NewWebSocketChannel creates a WebSocket channel for real-time streaming.
func NewWebSocketChannel(cfg WebSocketChannelConfig) *WebSocketChannel {
	cfg = cfg.withDefaults()
	return &WebSocketChannel{
		cfg:    cfg,
		logger: cfg.Logger.With(slog.String("component", "helixon.channel.ws")),
	}
}

func (ws *WebSocketChannel) Name() string { return "websocket" }

// Serve starts the WebSocket server. Full WebSocket upgrade handling requires
// gorilla/websocket; this scaffold provides the HTTP shell that will be
// completed when Go module resolution is available.
func (ws *WebSocketChannel) Serve(ctx context.Context, handler MessageHandler) error { //nolint:revive // unused-parameter required by interface
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ws.scaffoldHandler())

	ws.server = &http.Server{ //nolint:gosec // G112 slow ListenAndServe acceptable for dev-mode
		Addr:    ws.cfg.Addr,
		Handler: mux,
	}

	ws.logger.Info("WebSocket channel listening", slog.String("addr", ws.cfg.Addr))
	return runServerUntilCancel(ctx, ws.server)
}

func (ws *WebSocketChannel) Shutdown(ctx context.Context) error {
	if ws.server == nil {
		return nil
	}
	return ws.server.Shutdown(ctx)
}

// scaffoldHandler returns the /ws handler used while the WebSocket upgrade is
// not yet wired. It is exposed package-internal so the contract can be locked
// under regression test without binding to a TCP port.
//
// Contract: until gorilla/websocket (or an equivalent stdlib upgrade) lands,
// /ws responds 501 with `{"error":"WebSocket upgrade not yet implemented..."}`.
// Any change to that contract must be a deliberate sweep with tests updated.
func (ws *WebSocketChannel) scaffoldHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "WebSocket upgrade not yet implemented; requires gorilla/websocket",
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "")
	_ = enc.Encode(v)
}

// runServerUntilCancel starts srv.ListenAndServe() in a goroutine and waits
// for either a server error or ctx cancellation. It returns nil on graceful
// cancellation (the standard pattern across HTTPChannel and WebSocketChannel
// in this package). tech-debt-block-8.
func runServerUntilCancel(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

// CLIChannelAdapter wraps the existing channel/repl.REPL as a Channel interface.
type CLIChannelAdapter struct {
	name string
	run  func(ctx context.Context, handler MessageHandler) error
}

// NewCLIChannel creates a Channel adapter for the CLI REPL. The provided
// runFunc should start the REPL loop; it receives the MessageHandler for
// dispatching user input to the agent.
func NewCLIChannel(runFunc func(ctx context.Context, handler MessageHandler) error) *CLIChannelAdapter {
	return &CLIChannelAdapter{
		name: "cli",
		run:  runFunc,
	}
}

func (c *CLIChannelAdapter) Name() string { return c.name }

func (c *CLIChannelAdapter) Serve(ctx context.Context, handler MessageHandler) error {
	return c.run(ctx, handler)
}

func (c *CLIChannelAdapter) Shutdown(_ context.Context) error {
	return nil
}

// ChannelInfo returns a summary of a channel for diagnostics.
type ChannelInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// DescribeChannels returns info about all registered channels.
func DescribeChannels(channels []Channel) []ChannelInfo {
	infos := make([]ChannelInfo, len(channels))
	for i, ch := range channels {
		infos[i] = ChannelInfo{
			Name:   ch.Name(),
			Status: "registered",
		}
	}
	return infos
}

// multiplexChannels is a test helper that verifies all channels satisfy the interface.
func multiplexChannels(channels []Channel) error {
	seen := make(map[string]bool, len(channels))
	for _, ch := range channels {
		if seen[ch.Name()] {
			return fmt.Errorf("duplicate channel name: %s", ch.Name())
		}
		seen[ch.Name()] = true
	}
	return nil
}
