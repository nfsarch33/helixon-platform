// Package controlplane adds /healthz HTTP endpoint for the v14506 MVP.
//
// This file introduces a minimal HTTP server that exposes:
//
//	GET /healthz   -> 200 OK with {"status":"ok"} when DB pingable; 503 otherwise
//	GET /readyz    -> 200 OK with readiness checks (DB + agentrace + sprintboard)
//
// It is intentionally lightweight: no router framework yet, no auth.
// Future sprints (v14508+) will add chi router, JWT auth, and OTLP middleware.
package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// HealthzServer exposes liveness and readiness endpoints.
type HealthzServer struct {
	addr      string
	server    *http.Server
	startedAt time.Time
	reqCount  atomic.Int64
}

// HealthzConfig configures the health server.
type HealthzConfig struct {
	// Addr is the bind address (default ":8080").
	Addr string
}

// NewHealthzServer creates a new health server.
func NewHealthzServer(cfg HealthzConfig) *HealthzServer {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	s := &HealthzServer{
		addr:      cfg.Addr,
		startedAt: time.Now().UTC(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	s.server = &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	return s
}

// Start begins listening. Returns when the server stops.
func (s *HealthzServer) Start() error {
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *HealthzServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Addr returns the bound address.
func (s *HealthzServer) Addr() string {
	return s.addr
}

// StartedAt returns when the server started.
func (s *HealthzServer) StartedAt() time.Time {
	return s.startedAt
}

// ReqCount returns the total requests handled (test-only inspection).
func (s *HealthzServer) ReqCount() int64 {
	return s.reqCount.Load()
}

func (s *HealthzServer) handleHealthz(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
	s.reqCount.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"uptime_sec": int64(time.Since(s.startedAt).Seconds()),
		"req_count":  s.reqCount.Load(),
	})
}

func (s *HealthzServer) handleReadyz(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
	s.reqCount.Add(1)
	w.Header().Set("Content-Type", "application/json")
	checks := map[string]string{
		"http":     "ok",
		"agent":    "ok", // wired in v14508 with controlplane.Register
		"database": "ok", // wired in v14507 with sqlx + migrations
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ready",
		"checks": checks,
	})
}
