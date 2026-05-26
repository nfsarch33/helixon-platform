// Package dashboard exposes a small read-only HTTP surface that summarises
// the current state of a Helixon Runtime so operators can poll a single
// endpoint instead of grepping logs. The handler is deliberately
// dependency-light: it accepts any RuntimeView so tests can drive it with a
// fake without standing up the full agent stack.
package dashboard

import (
	"encoding/json"
	"net/http"
	"time"
)

// RuntimeView is the read-only contract the dashboard handler depends on.
// internal/helixon.Runtime satisfies it; tests can supply a fake.
type RuntimeView interface {
	AgentID() string
	Phase() string
	HeartbeatEvery() time.Duration
	ChannelCount() int
	RegisteredToolCount() int
}

// DashboardResponse is the JSON shape returned by /api/v1/dashboard.
type DashboardResponse struct {
	AgentID        string `json:"agent_id"`
	Phase          string `json:"phase"`
	HeartbeatEvery string `json:"heartbeat_every"`
	Channels       int    `json:"channels"`
	Tools          int    `json:"tools"`
	GeneratedAt    string `json:"generated_at"`
}

// Handler returns an http.Handler that serves the dashboard JSON. A nil
// RuntimeView yields a stub that reports phase=unknown so the endpoint
// never panics on a half-wired runtime.
func Handler(rv RuntimeView) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		resp := DashboardResponse{
			Phase:       "unknown",
			GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if rv != nil {
			resp.AgentID = rv.AgentID()
			resp.Phase = rv.Phase()
			resp.HeartbeatEvery = rv.HeartbeatEvery().String()
			resp.Channels = rv.ChannelCount()
			resp.Tools = rv.RegisteredToolCount()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// Mount registers the dashboard handler at /api/v1/dashboard on mux.
func Mount(mux *http.ServeMux, rv RuntimeView) {
	if mux == nil {
		return
	}
	mux.Handle("/api/v1/dashboard", Handler(rv))
}
