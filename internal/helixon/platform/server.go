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
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

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
	Addr                 string
	TenantID             string // v18686-1: multi-tenancy
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	HeartbeatInterval    time.Duration // SSE keepalive interval; default 15s
	StreamHandler        StreamHandler
	Logger               *slog.Logger
	PrometheusRegisterer prometheus.Registerer // optional: when set, /metrics exposes this registerer
}

// ReadinessGate reports whether the server is ready to serve traffic.
// The platform HTTP server uses this to gate /readyz responses (503 when
// not ready, 200 when ready). Implemented by callers to plug in checks
// for Sprintboard reachability, registry state, model availability, etc.
type ReadinessGate interface {
	Ready() (ready bool, reason string)
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

	mu         sync.Mutex
	listener   net.Listener
	boundAddr  string
	httpSrv    *http.Server
	readiness  ReadinessGate
	requestCnt *prometheus.CounterVec
	requestDur *prometheus.HistogramVec
}

// NewServer constructs a server that delegates blocking requests to handler
// and (when configured) streaming requests to cfg.StreamHandler.
func NewServer(cfg Config, handler helixon.MessageHandler) *Server {
	cfg = cfg.withDefaults()
	s := &Server{
		cfg:     cfg,
		handler: handler,
		logger:  cfg.Logger.With(slog.String("component", "helixon.platform")),
	}
	s.requestCnt = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "platform_http_requests_total",
		Help: "Total HTTP requests served by the platform, labeled by route, method, status.",
	}, []string{"route", "method", "status"})
	s.requestDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "platform_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds, labeled by route and method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method"})
	if cfg.PrometheusRegisterer != nil {
		cfg.PrometheusRegisterer.MustRegister(s.requestCnt, s.requestDur)
	}
	return s
}

// SetReadinessGate attaches a readiness gate to the server. The /readyz
// endpoint will return 200 + {"ready":true,...} when gate.Ready() reports
// ready; otherwise 503 + the gate's reason.
func (s *Server) SetReadinessGate(gate ReadinessGate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readiness = gate
}

// Routes returns the http.ServeMux without binding a listener. Useful for
// httptest-driven contract tests.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/messages/stream", s.handleStream)
	if g, ok := s.cfg.PrometheusRegisterer.(prometheus.Gatherer); ok {
		mux.Handle("GET /metrics", promhttp.HandlerFor(g, promhttp.HandlerOpts{}))
	} else {
		mux.Handle("GET /metrics", promhttp.Handler())
	}
	return s.instrument(mux)
}

// instrument wraps the mux with Prometheus request counters + duration
// histograms. Status code is captured via a ResponseWriter wrapper.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.URL.Path
		s.requestCnt.WithLabelValues(route, r.Method, http.StatusText(rec.status)).Inc()
		s.requestDur.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder captures the HTTP status code written by downstream
// handlers so the instrumentation middleware can label metrics correctly.
// It also forwards Flusher / Hijacker so streaming endpoints (SSE) and
// WebSocket upgrades continue to work under instrumentation.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter so net/http's
// http.ResponseController (and stdlib helpers) can locate Flusher /
// Hijacker without us re-implementing each interface.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
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
		//nolint:contextcheck // intentional detach; parent ctx is already cancelled
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:contextcheck
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

// handleReady reports the readiness state. When no gate is attached the
// server is considered ready by default. With a gate, the response is
// 200 + {"ready":true,"reason":"..."} on ready, or 503 + reason on
// not-ready. Operators wire gates by calling SetReadinessGate before
// Serve.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	gate := s.readiness
	s.mu.Unlock()
	if gate == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true, "reason": "no gate configured"})
		return
	}
	ready, reason := gate.Ready()
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ready": ready, "reason": reason})
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

	writeSSEHeaders(w)

	ctx := r.Context()
	heartbeatInterval := s.cfg.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15 * time.Second
	}

	var writeMu sync.Mutex
	stopHeartbeat := newHeartbeatLoop(&writeMu, &sseBuf{w: w, flush: flusher}, heartbeatInterval, nil)
	defer stopHeartbeat()

	emit := newEmitFn(ctx, &writeMu, &sseBuf{w: w, flush: flusher})

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

	writeStreamTerminal(w, flusher, streamErr, ctx.Err(), req.SessionID, s.logger)
}

// writeSSEHeaders sets the SSE prelude headers and writes 200 OK.
func writeSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// safeOneLine escapes newlines so SSE event payloads stay on a single line.
func safeOneLine(s string) string {
	return strings.ReplaceAll(s, "\n", "\\n")
}

// sseBuf is a tiny io.Writer wrapper around the http.ResponseWriter +
// Flusher pair used by the stream handlers. All writes go through
// writeMu under the caller-supplied mutex; tests can pass any io.Writer.
type sseBuf struct {
	w     io.Writer
	flush http.Flusher
}

func (b *sseBuf) Write(p []byte) (int, error) {
	n, err := b.w.Write(p)
	if b.flush != nil {
		b.flush.Flush()
	}
	return n, err
}

// newHeartbeatLoop starts a goroutine that emits ": heartbeat\n\n" every
// interval until stop() is invoked. The callback (if non-nil) is invoked
// after each successful write — used by the helper tests to capture
// writes without an http.ResponseWriter.
//
// Returns the stop func the caller MUST defer.
func newHeartbeatLoop(mu *sync.Mutex, w io.Writer, interval time.Duration, onWrite func()) func() {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				select {
				case <-done:
					return
				default:
				}
				mu.Lock()
				fmt.Fprint(w, ": heartbeat\n\n")
				mu.Unlock()
				if onWrite != nil {
					onWrite()
				}
			}
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
		wg.Wait()
	}
}

// newEmitFn returns a streaming emit closure that serialises writes through
// mu and surfaces ctx.Done as a wrapped "client disconnected" error.
func newEmitFn(ctx context.Context, mu *sync.Mutex, w io.Writer) func(string) error {
	return func(chunk string) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("client disconnected: %w", err)
		}
		safe := safeOneLine(chunk)
		mu.Lock()
		_, writeErr := fmt.Fprintf(w, "data: %s\n\n", safe)
		mu.Unlock()
		if writeErr != nil {
			return fmt.Errorf("write chunk: %w", writeErr)
		}
		return nil
	}
}

// writeStreamTerminal writes the SSE terminal event (event: error or
// event: done) after the handler returns. Holds the writeMu through the
// caller to keep ordering correct against a stopHeartbeat that may have
// raced.
func writeStreamTerminal(w http.ResponseWriter, flusher http.Flusher, streamErr error, ctxErr error, sessionID string, logger *slog.Logger) {
	if streamErr != nil {
		if ctxErr != nil {
			logger.Debug("stream aborted: client disconnected",
				slog.String("session", sessionID),
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
		return req, fmt.Errorf("invalid request body: %w", err)
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
