// Package mcp-doctor — MCP server health check (v18687-3).
//
// This package provides the per-server health probe + classification used
// by helix-dev-tools doctor mcp on wsl3 + windows-host.
//
// Design goals (per plan v18687-3 acceptance):
//
//   - Each probe respects a per-server timeout (default 3s; tests use 2s).
//   - Probes never block past deadline (no goroutine leaks per
//     harness-engineering-defaults.mdc).
//   - Classification is GREEN iff reachable AND HTTP 200 within timeout.
//     TCP-up + non-200 = RED. TCP-down = RED.
//   - Result.Environment records the surface name ("wsl3" / "windows-host")
//     so per-environment failures can be triaged cleanly.
package mcpdoctor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Status is the per-server classification.
type Status string

const (
	// StatusGreen = TCP reachable + HTTP 200 within timeout.
	StatusGreen Status = "GREEN"
	// StatusRed = TCP unreachable OR HTTP non-200 OR probe errored.
	StatusRed Status = "RED"
)

// Server describes one MCP server to probe.
type Server struct {
	Name       string
	Address    string // host:port
	HealthPath string // typically "/healthz"
}

// ServerResult is the per-server outcome.
type ServerResult struct {
	Name        string
	Address     string
	Status      Status
	Reachable   bool
	HTTPStatus  int
	Latency     time.Duration
	Error       error
	Environment string
}

// Result is the doctor run output.
type Result struct {
	Environment string
	Servers     []ServerResult
	Generated   time.Time
}

// Doctor is the per-environment MCP health checker.
//
// Construct with NewDoctor (live wsl3 set) or NewDoctorWithServers
// (test/custom). The Run method probes all configured servers in
// parallel with bounded concurrency (WaitGroup).
type Doctor struct {
	Env     string
	Servers []Server
}

// NewDoctor returns a Doctor configured for the named environment with
// the canonical MCP server list for that env.
//
// On wsl3, the canonical list comes from `helix-dev-tools doctor mcp`
// inventory: engram, sprintboard, svcregistryd, llm-cluster-router,
// helixon-mcp, helix-dev-tools webhook.
//
// On windows-host (the same machine running Cursor), the canonical list
// is the Cursor MCP servers: engram-oss-legacy, perplexity, fetch,
// context7, codegraph, time, duckduckgo, etc.
func NewDoctor(env string) *Doctor {
	switch env {
	case "wsl3":
		return &Doctor{
			Env: env,
			Servers: []Server{
				{Name: "engram", Address: "127.0.0.1:8280", HealthPath: "/healthz"},
				{Name: "sprintboard", Address: "127.0.0.1:8765", HealthPath: "/healthz"},
				{Name: "svcregistryd", Address: "127.0.0.1:8786", HealthPath: "/healthz"},
				{Name: "llm-cluster-router", Address: "127.0.0.1:8787", HealthPath: "/healthz"},
			},
		}
	case "windows-host":
		return &Doctor{
			Env: env,
			Servers: []Server{
				// Cursor MCP servers don't expose /healthz over HTTP; probe via the
				// loopback Cursor app server instead.
				{Name: "cursor", Address: "127.0.0.1:9222", HealthPath: "/json/version"},
			},
		}
	default:
		return &Doctor{Env: env}
	}
}

// NewDoctorWithServers returns a Doctor with an explicit server list
// (test entrypoint).
func NewDoctorWithServers(env string, servers []Server) *Doctor {
	return &Doctor{Env: env, Servers: servers}
}

// Run probes each server in parallel, bounded by per-probe timeout.
// Empty/nil servers returns a non-nil Result with empty Servers.
func (d *Doctor) Run(ctx context.Context, perProbeTimeout time.Duration) *Result {
	result := &Result{
		Environment: d.Env,
		Servers:     []ServerResult{},
		Generated:   time.Now(),
	}

	if len(d.Servers) == 0 {
		return result
	}

	if perProbeTimeout <= 0 {
		perProbeTimeout = 3 * time.Second
	}

	results := make([]ServerResult, len(d.Servers))
	var wg sync.WaitGroup
	wg.Add(len(d.Servers))

	for i, svc := range d.Servers {
		go func(idx int, s Server) {
			defer wg.Done()
			results[idx] = probeMCP(ctx, d.Env, s, perProbeTimeout)
		}(i, svc)
	}

	wg.Wait()
	result.Servers = results
	return result
}

// probeMCP runs one probe: TCP connect + HTTP GET health path. Returns
// a ServerResult with classification.
func probeMCP(parent context.Context, env string, svc Server, timeout time.Duration) ServerResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	host, port, err := net.SplitHostPort(svc.Address)
	if err != nil {
		return ServerResult{
			Name: svc.Name, Address: svc.Address,
			Status: StatusRed, Reachable: false,
			Latency: time.Since(start), Error: err,
			Environment: env,
		}
	}

	// TCP dial with context to honour per-probe timeout.
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	latency := time.Since(start)
	if err != nil {
		return ServerResult{
			Name: svc.Name, Address: svc.Address,
			Status: StatusRed, Reachable: false,
			Latency:     latency,
			Error:       fmt.Errorf("dial %s: %w", svc.Address, err),
			Environment: env,
		}
	}
	_ = conn.Close()

	// TCP up; now HTTP GET.
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
		return ServerResult{
			Name: svc.Name, Address: svc.Address,
			Status: StatusRed, Reachable: true,
			Latency:     time.Since(start),
			Error:       fmt.Errorf("build req %s: %w", url, err),
			Environment: env,
		}
	}
	resp, err := client.Do(req)
	latency = time.Since(start)
	if err != nil {
		return ServerResult{
			Name: svc.Name, Address: svc.Address,
			Status: StatusRed, Reachable: true,
			Latency:     latency,
			Error:       fmt.Errorf("http %s: %w", url, err),
			Environment: env,
		}
	}
	defer resp.Body.Close()

	status := StatusRed
	if resp.StatusCode == http.StatusOK {
		status = StatusGreen
	}
	return ServerResult{
		Name: svc.Name, Address: svc.Address,
		Status: status, Reachable: true,
		HTTPStatus: resp.StatusCode, Latency: latency,
		Environment: env,
	}
}
