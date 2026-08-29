package helixon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	// Gatherer, when set, makes the channel serve GET /metrics from it.
	//
	// This is the agent's ONLY exposition path. platform.Server has a
	// PrometheusRegisterer field, but that server is a different process
	// surface (`helixon platform`, :8787) and serve mode never starts it —
	// which is why :8686/metrics answered 404 on a live agent while the
	// unset field made it look wired. Left nil, /metrics stays absent rather
	// than serving the process-global default registry, so a scrape can
	// never mistake "Go runtime metrics from a binary with no agent" for
	// "the agent is reporting".
	Gatherer prometheus.Gatherer
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

	mu        sync.Mutex
	boundAddr string
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

// Routes returns the channel's handler without binding a listener, so the
// route table can be asserted directly. Modeled on platform.Server.Routes.
//
// /healthz and /metrics are siblings of the pre-existing /api/v1/health for a
// reason: /api/v1/health is the channel's own liveness answer, while /healthz
// and /metrics are what every probe, scraper and systemd unit in this estate
// already looks for. An endpoint nobody's tooling asks for is an endpoint that
// might as well not exist — which is exactly the state the agent was in.
func (h *HTTPChannel) Routes(handler MessageHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/chat", h.chatHandler(handler))
	mux.HandleFunc("GET /api/v1/health", h.healthHandler())
	mux.HandleFunc("GET /healthz", h.healthHandler())
	if h.cfg.Gatherer != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(h.cfg.Gatherer, promhttp.HandlerOpts{}))
	}
	return mux
}

func (h *HTTPChannel) Serve(ctx context.Context, handler MessageHandler) error {
	// net.Listen rather than ListenAndServe so a ":0" bind can report the
	// port it actually got. Without that, an end-to-end test of the metrics
	// endpoint has to hard-code a port and race every other test in the tree
	// for it.
	ln, err := net.Listen("tcp", h.cfg.Addr)
	if err != nil {
		return fmt.Errorf("helixon: http channel listen %s: %w", h.cfg.Addr, err)
	}

	srv := &http.Server{
		Addr:         h.cfg.Addr,
		Handler:      h.Routes(handler),
		ReadTimeout:  h.cfg.ReadTimeout,
		WriteTimeout: h.cfg.WriteTimeout,
	}
	// Published under the mutex: Shutdown runs on another goroutine, and a
	// bound listener nobody can safely observe is not much better than none.
	h.mu.Lock()
	h.server = srv
	h.boundAddr = ln.Addr().String()
	h.mu.Unlock()

	h.logger.Info("HTTP channel listening", slog.String("addr", ln.Addr().String()))
	return serveListenerUntilCancel(ctx, srv, ln)
}

// BoundAddr returns the address the channel actually bound, or "" before
// Serve has bound one.
func (h *HTTPChannel) BoundAddr() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.boundAddr
}

func (h *HTTPChannel) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	srv := h.server
	h.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
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
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// serveListenerUntilCancel is runServerUntilCancel for a caller that already
// bound the listener (so it can report the chosen port).
func serveListenerUntilCancel(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
