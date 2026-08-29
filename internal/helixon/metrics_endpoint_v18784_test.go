package helixon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
)

// The agent binds :8686 in production and answered 404 on BOTH /healthz and
// /metrics: the HTTP channel served only /api/v1/chat and /api/v1/health, and
// the PrometheusRegisterer field that looked like the wiring belongs to
// platform.Server on :8787, which serve mode never starts. These tests drive
// the channel that is actually bound.

func echoHandler(_ context.Context, msg IncomingMessage) (string, error) {
	return "echo:" + msg.Content, nil
}

func startChannel(t *testing.T, cfg HTTPChannelConfig) string {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	ch := NewHTTPChannel(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := ch.Serve(ctx, echoHandler); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = ch.Shutdown(context.Background())
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("HTTP channel did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := ch.BoundAddr(); addr != "" {
			return "http://" + addr
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("HTTP channel never reported a bound address")
	return ""
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

// TestHTTPChannelServesHealthzAndMetrics is the acceptance test for S-05's
// exposition half: a real listener, on the real channel serve mode binds,
// answering the two paths every probe and scraper in this estate looks for.
func TestHTTPChannelServesHealthzAndMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := agentmetrics.New(reg, "abc123"); err != nil {
		t.Fatalf("agentmetrics.New: %v", err)
	}
	base := startChannel(t, HTTPChannelConfig{Gatherer: reg})

	if code, _ := get(t, base+"/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200; nothing can liveness-check an agent that 404s here", code)
	}
	// The pre-existing path must keep working: /healthz is an addition, not a
	// rename, and something out there is already calling it.
	if code, _ := get(t, base+"/api/v1/health"); code != http.StatusOK {
		t.Errorf("GET /api/v1/health = %d, want 200", code)
	}

	code, body := get(t, base+"/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", code)
	}
	for _, name := range agentmetrics.Names() {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics does not expose %q; the contract lists it", name)
		}
	}
	if !strings.Contains(body, `revision="abc123"`) {
		t.Error("/metrics does not carry the build revision; a scrape cannot identify the running build")
	}
}

// TestHTTPChannelWithoutGathererServesNoMetrics is the positive control for the
// test above.
//
// Revert the wiring — drop Gatherer on the way to the channel — and /metrics
// must go back to 404. Without this assertion the suite would still pass if
// someone "fixed" the endpoint by serving the process-global default registry,
// which would answer 200 with Go runtime metrics from a binary running no agent
// at all: a green scrape that proves nothing, which is worse than a 404.
func TestHTTPChannelWithoutGathererServesNoMetrics(t *testing.T) {
	base := startChannel(t, HTTPChannelConfig{})

	if code, _ := get(t, base+"/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200 even with metrics off; liveness is not conditional on metrics", code)
	}
	code, body := get(t, base+"/metrics")
	if code == http.StatusOK {
		t.Fatalf("GET /metrics = 200 with no gatherer wired; the endpoint is answering from somewhere it should not (body starts %q)",
			body[:min(len(body), 120)])
	}
	if code != http.StatusNotFound {
		t.Errorf("GET /metrics = %d with no gatherer, want 404", code)
	}
}

// TestHTTPChannelRoutesMatchTheServedListener guards the split between Routes()
// and Serve(): a route added to one and not the other is exactly the kind of
// gap that put the agent in this state.
func TestHTTPChannelRoutesMatchTheServedListener(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := agentmetrics.New(reg, "r"); err != nil {
		t.Fatalf("agentmetrics.New: %v", err)
	}
	ch := NewHTTPChannel(HTTPChannelConfig{Gatherer: reg})
	srv := httptest.NewServer(ch.Routes(echoHandler))
	t.Cleanup(srv.Close)

	live := startChannel(t, HTTPChannelConfig{Gatherer: reg})
	for _, path := range []string{"/healthz", "/api/v1/health", "/metrics"} {
		viaRoutes, _ := get(t, srv.URL+path)
		viaListener, _ := get(t, live+path)
		if viaRoutes != viaListener {
			t.Errorf("%s: Routes() answered %d but the bound listener answered %d", path, viaRoutes, viaListener)
		}
	}
}
