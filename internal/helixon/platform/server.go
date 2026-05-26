// Package platform implements the Helixon platform HTTP server (v8900-B6/B7).
//
// It exposes a small, transport-only surface that delegates message
// processing to a helixon.MessageHandler, reusing the existing Runtime
// dispatch path. This avoids a second LLM seam: the runtime owns the LLM
// provider, the registry, and the agent loop; this package only exposes
// HTTP and SSE shells.
//
// Endpoints:
//
//	GET  /healthz             -> {"status":"ok"}
//	POST /v1/messages         -> blocking JSON request/response
//	POST /v1/messages/stream  -> SSE; one `data:` line per chunk + `event: done`
//
// The default bind address is 127.0.0.1:8787; override with HELIXON_PORT
// or pass Config.Addr. New in v8900-B6.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
)

// DefaultAddr is the canonical bind address for the platform HTTP server.
const DefaultAddr = "127.0.0.1:8787"

// StreamHandler emits chunks of a streamed completion to the SSE writer
// via emit. Returning a non-nil error aborts the stream; the server emits
// `event: error` followed by the error message.
type StreamHandler func(ctx context.Context, msg helixon.IncomingMessage, emit func(chunk string) error) error

// Config configures Server.
type Config struct {
	Addr              string
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	HeartbeatInterval time.Duration // SSE keepalive interval; default 15s
	StreamHandler     StreamHandler
	Logger            *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = 30 * time.Second
	}
	if c.WriteTimeout <= 0 {
		// SSE streams may run longer; do not bound write timeout aggressively.
		c.WriteTimeout = 0
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 120 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Server hosts the Helixon platform HTTP API.
type Server struct {
	cfg     Config
	handler helixon.MessageHandler
	logger  *slog.Logger

	mu        sync.Mutex
	listener  net.Listener
	boundAddr string
	httpSrv   *http.Server
}

// NewServer constructs a server that delegates blocking requests to handler
// and (when configured) streaming requests to cfg.StreamHandler.
func NewServer(cfg Config, handler helixon.MessageHandler) *Server {
	cfg = cfg.withDefaults()
	return &Server{
		cfg:     cfg,
		handler: handler,
		logger:  cfg.Logger.With(slog.String("component", "helixon.platform")),
	}
}

// Routes returns the http.ServeMux without binding a listener. Useful for
// httptest-driven contract tests.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/messages/stream", s.handleStream)
	return mux
}

// Serve binds the configured address and serves until ctx is cancelled.
// Use BoundAddr after Serve has started to retrieve the actual port (e.g.
// when Addr ends with ":0").
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("platform: listen %s: %w", s.cfg.Addr, err)
	}

	s.mu.Lock()
	s.listener = ln
	s.boundAddr = ln.Addr().String()
	s.httpSrv = &http.Server{
		Handler:      s.Routes(),
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
		IdleTimeout:  s.cfg.IdleTimeout,
	}
	srv := s.httpSrv
	s.mu.Unlock()

	s.logger.Info("platform listening", slog.String("addr", s.boundAddr))

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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}

// BoundAddr returns the actual listen address (host:port). Empty string
// before Serve has bound the listener.
func (s *Server) BoundAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundAddr
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type messageRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Content   string `json:"content"`
}

type messageResponse struct {
	SessionID string `json:"session_id,omitempty"`
	Response  string `json:"response"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	req, err := decodeMessageRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	msg := helixon.IncomingMessage{
		SessionID: req.SessionID,
		Channel:   "platform",
		Content:   req.Content,
	}

	resp, err := s.handler(r.Context(), msg)
	if err != nil {
		s.logger.Warn("messages: handler error", slog.String("error", err.Error()))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, messageResponse{
		SessionID: msg.SessionID,
		Response:  resp,
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.cfg.StreamHandler == nil {
		writeError(w, http.StatusNotImplemented, "streaming not configured")
		return
	}
	req, err := decodeMessageRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported by transport")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	heartbeatInterval := s.cfg.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15 * time.Second
	}

	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	// writeMu serialises all writes to the ResponseWriter (heartbeat + emit).
	var writeMu sync.Mutex

	heartbeatDone := make(chan struct{})
	var heartbeatWG sync.WaitGroup
	heartbeatWG.Add(1)
	stopHeartbeat := func() {
		select {
		case <-heartbeatDone:
		default:
			close(heartbeatDone)
		}
		heartbeatWG.Wait()
	}
	defer stopHeartbeat()

	go func() {
		defer heartbeatWG.Done()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-heartbeatTicker.C:
				select {
				case <-heartbeatDone:
					return
				default:
				}
				writeMu.Lock()
				fmt.Fprint(w, ": heartbeat\n\n")
				flusher.Flush()
				writeMu.Unlock()
			}
		}
	}()

	emit := func(chunk string) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("client disconnected: %w", err)
		}
		safe := strings.ReplaceAll(chunk, "\n", "\\n")
		writeMu.Lock()
		_, writeErr := fmt.Fprintf(w, "data: %s\n\n", safe)
		flusher.Flush()
		writeMu.Unlock()
		if writeErr != nil {
			return fmt.Errorf("write chunk: %w", writeErr)
		}
		return nil
	}

	msg := helixon.IncomingMessage{
		SessionID: req.SessionID,
		Channel:   "platform-stream",
		Content:   req.Content,
	}

	streamErr := s.cfg.StreamHandler(ctx, msg, emit)

	// Stop heartbeat before any terminal writes to prevent races.
	stopHeartbeat()
	writeMu.Lock()
	defer writeMu.Unlock()

	if streamErr != nil {
		if ctx.Err() != nil {
			s.logger.Debug("stream aborted: client disconnected",
				slog.String("session", req.SessionID),
			)
			return
		}
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", strings.ReplaceAll(streamErr.Error(), "\n", " "))
		flusher.Flush()
		return
	}
	fmt.Fprint(w, "event: done\ndata: [DONE]\n\n")
	flusher.Flush()
}

func decodeMessageRequest(r *http.Request) (messageRequest, error) {
	var req messageRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("invalid request body: %v", err)
	}
	if strings.TrimSpace(req.Content) == "" {
		return req, errors.New("content is required")
	}
	return req, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
