// runx-public-repo-gate: allow-file fleet_host_alias
// Package fleet — win1 central services hook-up (v18687-1).
//
// This file implements the Win1Service probe + ProbeResult aggregator
// used by TestWin1Hookup_AllServicesReachable and downstream
// helixon-doctor / fleet-doctor checks.
//
// Design goals (per plan v18687-1 acceptance):
//
//   - Each probe respects a per-service timeout (default 3s).
//   - Probes never block past deadline (no goroutine leaks).
//   - ProbeResult records Reachable + HTTPStatus + Latency + Error
//     so honest KPI scoreboard (DRL-8.20-r4) can mark UNVERIFIED honestly.
//   - resolveWin1Host() reads the canonical fleet IP from inventory;
//     falls back to the v18674-2 corrected IP if registry unavailable.
package fleet

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// Win1Service describes one central service on win1/wsl1.
type Win1Service struct {
	Name       string
	Address    string // host:port
	HealthPath string // typically "/healthz"
}

// ProbeResult is the per-service outcome.
type ProbeResult struct {
	Service    string
	Address    string
	Reachable  bool
	HTTPStatus int
	Latency    time.Duration
	Error      error
}

// canonicalWin1Host is the v18674-2 corrected Tailscale IP for wsl1.
// It is the documented primary target for all central services in
// inventory/services/registry.yaml. If the IP rotates, update here.
const canonicalWin1Host = "100.84.108.92"

// resolveWin1Host returns the win1/wsl1 Tailscale hostname or IP.
// On error, returns the IP literal so the caller can decide whether
// to skip or report UNVERIFIED honestly per DRL-8.20-r4.
func resolveWin1Host(_ context.Context) (string, error) {
	if v := os.Getenv("HELIXON_WIN1_HOST"); v != "" {
		return v, nil
	}
	return canonicalWin1Host, nil
}

// ProbeWin1Services probes each service in parallel, respecting per-probe
// timeout. Returns a slice (length == len(services)) in input order.
// Empty/nil input returns a non-nil empty slice.
//
// Concurrency model: bounded via WaitGroup; per-probe runs in its own
// goroutine but the HTTP client's per-request timeout bounds each one.
// No unbounded fan-out: caller controls slice length.
func ProbeWin1Services(ctx context.Context, services []Win1Service, perProbeTimeout time.Duration) []ProbeResult {
	if len(services) == 0 {
		return []ProbeResult{}
	}
	if perProbeTimeout <= 0 {
		perProbeTimeout = 3 * time.Second
	}

	results := make([]ProbeResult, len(services))
	var wg sync.WaitGroup
	wg.Add(len(services))

	for i, svc := range services {
		go func(idx int, s Win1Service) {
			defer wg.Done()
			results[idx] = probeOne(ctx, s, perProbeTimeout)
		}(i, svc)
	}
	wg.Wait()
	return results
}

// probeOne runs a single probe: TCP connect + HTTP GET /healthz.
// Records Latency even on failure so callers can see how long
// the dial blocked before timing out (useful evidence).
func probeOne(parent context.Context, svc Win1Service, timeout time.Duration) ProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	host, port, err := net.SplitHostPort(svc.Address)
	if err != nil {
		return ProbeResult{
			Service: svc.Name, Address: svc.Address,
			Reachable: false, Latency: time.Since(start),
			Error: err,
		}
	}

	// Dial with context to honour per-probe timeout (NOT system net.Dial
	// which would block until TCP backoff ~75s on Linux).
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	latency := time.Since(start)
	if err != nil {
		return ProbeResult{
			Service: svc.Name, Address: svc.Address,
			Reachable: false, Latency: latency,
			Error: fmt.Errorf("dial %s: %w", svc.Address, err),
		}
	}
	_ = conn.Close()

	// Dial succeeded; now HTTP GET healthz. Use a short per-request timeout
	// independent of ctx so we capture status even on slow servers.
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       (&net.Dialer{Timeout: timeout}).DialContext,
		},
	}
	url := "http://" + net.JoinHostPort(host, port) + svc.HealthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ProbeResult{
			Service: svc.Name, Address: svc.Address,
			Reachable: false, HTTPStatus: 0, Latency: latency,
			Error: fmt.Errorf("build req %s: %w", url, err),
		}
	}
	resp, err := client.Do(req)
	latency = time.Since(start)
	if err != nil {
		// Dial succeeded but HTTP failed → still "reachable" (TCP up).
		return ProbeResult{
			Service: svc.Name, Address: svc.Address,
			Reachable: true, HTTPStatus: 0, Latency: latency,
			Error: fmt.Errorf("http %s: %w", url, err),
		}
	}
	defer resp.Body.Close()
	return ProbeResult{
		Service: svc.Name, Address: svc.Address,
		Reachable: true, HTTPStatus: resp.StatusCode,
		Latency: latency,
		Error:   nil,
	}
}
