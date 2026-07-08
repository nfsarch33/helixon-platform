package channel

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// WebhookConfig configures the HTTP webhook channel.
type WebhookConfig struct {
	BearerToken string
	MaxBodySize int64
	AgentID     string
	Logger      *slog.Logger
}

func (c WebhookConfig) withDefaults() WebhookConfig {
	if c.MaxBodySize <= 0 {
		c.MaxBodySize = 256 * 1024
	}
	if c.AgentID == "" {
		c.AgentID = "helixon-webhook"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// WebhookRequest is the JSON payload for incoming webhook messages.
type WebhookRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

// WebhookResponse is the JSON response from the webhook handler.
type WebhookResponse struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response"`
	Elapsed   string `json:"elapsed"`
	Error     string `json:"error,omitempty"`
}

// WebhookHandler implements an HTTP webhook channel with bearer auth.
type WebhookHandler struct {
	agent  AgentRunner
	cfg    WebhookConfig
	logger *slog.Logger
}

// NewWebhookHandler creates a webhook channel handler.
func NewWebhookHandler(agent AgentRunner, cfg WebhookConfig) *WebhookHandler {
	cfg = cfg.withDefaults()
	return &WebhookHandler{
		agent:  agent,
		cfg:    cfg,
		logger: cfg.Logger.With(slog.String("component", "helixon.channel.webhook")),
	}
}

// ServeHTTP handles incoming webhook requests.
func (w *WebhookHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if w.cfg.BearerToken != "" {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + w.cfg.BearerToken
		if !strings.HasPrefix(auth, "Bearer ") ||
			subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, w.cfg.MaxBodySize+1))
	_ = r.Body.Close()
	if err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > w.cfg.MaxBodySize {
		http.Error(rw, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	var req WebhookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(rw, http.StatusBadRequest, WebhookResponse{Error: "invalid JSON"})
		return
	}
	if req.Message == "" {
		writeJSON(rw, http.StatusBadRequest, WebhookResponse{Error: "message is required"})
		return
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID, err = w.agent.CreateSession(r.Context(), w.cfg.AgentID)
		if err != nil {
			w.logger.Error("create session failed", slog.String("error", err.Error()))
			writeJSON(rw, http.StatusInternalServerError, WebhookResponse{Error: "session creation failed"})
			return
		}
	}

	start := time.Now()
	response, err := w.agent.Run(r.Context(), sessionID, req.Message)
	elapsed := time.Since(start)

	if err != nil {
		w.logger.Warn("agent run failed",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		writeJSON(rw, http.StatusInternalServerError, WebhookResponse{
			SessionID: sessionID,
			Error:     err.Error(),
			Elapsed:   elapsed.String(),
		})
		return
	}

	writeJSON(rw, http.StatusOK, WebhookResponse{
		SessionID: sessionID,
		Response:  response,
		Elapsed:   elapsed.Round(time.Millisecond).String(),
	})
}

// Handler returns the http.Handler for mounting on a router.
func (w *WebhookHandler) Handler() http.Handler {
	return w
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Router builds a complete HTTP mux with the webhook endpoint and health check.
func Router(webhook *WebhookHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/v1/chat", webhook)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	return mux
}
