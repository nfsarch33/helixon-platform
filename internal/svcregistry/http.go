package svcregistry

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTPServer wires a Registry to a Prometheus /metrics endpoint plus a
// small JSON API for human consumers.
//
//	GET  /healthz                            liveness
//	GET  /metrics                            Prometheus scrape
//	GET  /api/v1/services                    full snapshot
//	GET  /api/v1/services?host=<h>           filtered by host
//	GET  /api/v1/services/<host>/<name>      single entry
//	POST /api/v1/services                    JSON body → Register
//	DELETE /api/v1/services/<host>/<name>    Unregister
//	GET  /api/v1/conflicts                   List of port-collision groups
type HTTPServer struct {
	reg *Registry

	mux *http.ServeMux
}

// NewHTTPServer returns a server wired to reg. The returned handler is
// safe for http.Server.
func NewHTTPServer(reg *Registry) *HTTPServer {
	s := &HTTPServer{
		reg: reg,
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler returns the http.Handler.
func (s *HTTPServer) Handler() http.Handler { return s.mux }

func (s *HTTPServer) routes() {
	s.mux.HandleFunc("/healthz", s.healthz)
	// /metrics serves from the registry's private prometheus.Registry so
	// counter values for this specific registry are surfaced (the default
	// handler uses prometheus.DefaultRegisterer, which would be empty).
	s.mux.Handle("/metrics", promhttp.HandlerFor(s.reg.metrics.PROMReg, promhttp.HandlerOpts{}))
	s.mux.HandleFunc("/api/v1/services", s.servicesRoot)
	s.mux.HandleFunc("/api/v1/services/", s.servicesByPath)
	s.mux.HandleFunc("/api/v1/conflicts", s.conflicts)
}

func (s *HTTPServer) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *HTTPServer) servicesRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		host := r.URL.Query().Get("host")
		var snap []Service
		if host != "" {
			for _, s := range s.reg.List() {
				if s.Host == host {
					snap = append(snap, s)
				}
			}
		} else {
			snap = s.reg.List()
		}
		writeJSON(w, http.StatusOK, snap)
	case http.MethodPost:
		var svc Service
		if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := s.reg.Register(svc); err != nil {
			switch {
			case errors.Is(err, ErrPortConflict):
				writeError(w, http.StatusConflict, err.Error())
			default:
				writeError(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		writeJSON(w, http.StatusCreated, svc)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) servicesByPath(w http.ResponseWriter, r *http.Request) {
	// path is /api/v1/services/<host>/<name>
	p := r.URL.Path
	const prefix = "/api/v1/services/"
	if len(p) <= len(prefix) {
		http.NotFound(w, r)
		return
	}
	tail := p[len(prefix):]
	// split on first '/'
	host, name, ok := splitHostName(tail)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if svc, found := s.reg.Get(host, name); found {
			writeJSON(w, http.StatusOK, svc)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	case http.MethodDelete:
		if err := s.reg.Unregister(host, name); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) conflicts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.reg.Conflicts())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func splitHostName(s string) (host, name string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			if i == 0 || i == len(s)-1 {
				return "", "", false
			}
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}
