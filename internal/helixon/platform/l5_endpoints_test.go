package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
)

// stubReadinessGate returns a fixed Ready signal.
type stubReadinessGate struct {
	ready bool
	why   string
}

func (s stubReadinessGate) Ready() (bool, string) {
	return s.ready, s.why
}

func echoMsgHandler(_ context.Context, msg helixon.IncomingMessage) (string, error) {
	return "echo:" + msg.Content, nil
}

func newProbeServer(gate ReadinessGate) *httptest.Server {
	cfg := Config{Addr: "127.0.0.1:0", PrometheusRegisterer: prometheus.NewRegistry()}
	srv := NewServer(cfg, echoMsgHandler)
	if gate != nil {
		srv.SetReadinessGate(gate)
	}
	return httptest.NewServer(srv.Routes())
}

func TestHealthzEndpoint(t *testing.T) {
	t.Parallel()
	ts := newProbeServer(nil)
	defer func() { ts.Close() }()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: want 200, got %d", resp.StatusCode)
	}
}

func TestReadyzEndpointReady(t *testing.T) {
	t.Parallel()
	gate := stubReadinessGate{ready: true, why: "all dependencies healthy"}
	ts := newProbeServer(gate)
	defer func() { ts.Close() }()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ready gate should be 200, got %d", resp.StatusCode)
	}
}

func TestReadyzEndpointNotReady(t *testing.T) {
	t.Parallel()
	gate := stubReadinessGate{ready: false, why: "sprintboard URL not reachable"}
	ts := newProbeServer(gate)
	defer func() { ts.Close() }()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("not-ready gate should be 503, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	t.Parallel()
	ts := newProbeServer(nil)
	defer func() { ts.Close() }()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type: want text/plain prefix, got %q", ct)
	}
}

func TestMetricsEndpointRegistersPromHandler(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	cfg := Config{Addr: "127.0.0.1:0", PrometheusRegisterer: reg}
	srv := NewServer(cfg, echoMsgHandler)
	mux := srv.Routes()
	muxServer := httptest.NewServer(mux)
	defer func() { muxServer.Close() }()

	resp, err := http.Get(muxServer.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()

	metricsResp, err := http.Get(muxServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = metricsResp.Body.Close() }()
	if metricsResp.StatusCode != http.StatusOK {
		t.Errorf("metrics status: want 200, got %d", metricsResp.StatusCode)
	}
}
