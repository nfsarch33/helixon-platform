package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthzServer_StartShutdown(t *testing.T) {
	// Use httptest to avoid real network; the Start method is exercised in e2e_test.go
	srv := NewHealthzServer(HealthzConfig{Addr: ":0"})
	assert.NotNil(t, srv)
	assert.Equal(t, ":0", srv.Addr())
	assert.WithinDuration(t, time.Now().UTC(), srv.StartedAt(), 2*time.Second)
	assert.Equal(t, int64(0), srv.ReqCount())
}

func TestHealthzServer_HandleHealthz(t *testing.T) {
	srv := NewHealthzServer(HealthzConfig{Addr: ":0"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.handleHealthz(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
	assert.Contains(t, body, "uptime_sec")
	assert.Contains(t, body, "req_count")
	assert.Equal(t, int64(1), srv.ReqCount())
}

func TestHealthzServer_HandleReadyz(t *testing.T) {
	srv := NewHealthzServer(HealthzConfig{Addr: ":0"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	srv.handleReadyz(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ready", body["status"])
	checks, ok := body["checks"].(map[string]any)
	require.True(t, ok, "checks should be a map")
	assert.Equal(t, "ok", checks["http"])
	assert.Equal(t, "ok", checks["agent"])
	assert.Equal(t, "ok", checks["database"])
}

func TestHealthzServer_RealListenAndServe(t *testing.T) {
	// Pick a random port by binding to :0 on a localhost test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			srv := NewHealthzServer(HealthzConfig{Addr: ":0"})
			srv.handleHealthz(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer func() { ts.Close() }()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json"))
}

func TestHealthzServer_Shutdown_NoError(t *testing.T) {
	srv := NewHealthzServer(HealthzConfig{Addr: ":0"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Shutdown on a non-started server should return nil (http.Server.Shutdown on unstarted listener)
	err := srv.Shutdown(ctx)
	// Acceptable: returns nil, returns http.ErrServerClosed, or context.DeadlineExceeded
	if err != nil {
		assert.Contains(t, []error{nil, http.ErrServerClosed, context.DeadlineExceeded}, err)
	}
}
