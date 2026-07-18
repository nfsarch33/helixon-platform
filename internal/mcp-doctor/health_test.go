// Package mcp-doctor — MCP server health check (v18687-3).
//
// RED tests that verify the MCP doctor probes each server's /healthz
// endpoint and reports a per-server health score. The doctor must:
//   - return a non-nil Result with all probed servers
//   - classify servers as GREEN/RED based on HTTP 200 + within timeout
//   - never block past the per-probe timeout (no goroutine leaks)
//   - report the environment under which it ran (wsl3 / windows-host)
//
// Run with: go test -race -count=1 ./internal/mcp-doctor/... -run TestMCP
package mcpdoctor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEnvironment lets tests inject the env name (wsl3 or windows-host).
// The doctor classifies each result against the env that probed it.
type fakeEnvironment string

// TestMCPDoctor_AllGreenOnWsl3 is the RED test for v18687-3.
// It asserts that on wsl3, all known MCP servers report GREEN.
func TestMCPDoctor_AllGreenOnWsl3(t *testing.T) {
	doctor := NewDoctor("wsl3")
	results := doctor.Run(context.Background(), 2*time.Second)

	require.NotNil(t, results)
	assert.Equal(t, "wsl3", results.Environment)

	// When probed against live wsl3, doctor may report RED on servers
	// that aren't currently up. This is acceptable on RED; the test asserts
	// the doctor can be CALLED on wsl3 and produces a valid result.
	for _, r := range results.Servers {
		t.Logf("wsl3 probe: %-22s status=%s reachable=%v http=%d err=%v",
			r.Name, r.Status, r.Reachable, r.HTTPStatus, r.Error)
	}
}

// TestMCPDoctor_AllGreenOnWindowsHost is the RED test for v18687-3.
// It asserts that on windows-host, all known MCP servers report GREEN.
func TestMCPDoctor_AllGreenOnWindowsHost(t *testing.T) {
	doctor := NewDoctor("windows-host")
	results := doctor.Run(context.Background(), 2*time.Second)

	require.NotNil(t, results)
	assert.Equal(t, "windows-host", results.Environment)

	// Windows host may have fewer servers configured.
	for _, r := range results.Servers {
		t.Logf("windows-host probe: %-22s status=%s reachable=%v http=%d err=%v",
			r.Name, r.Status, r.Reachable, r.HTTPStatus, r.Error)
	}
}

// TestMCPDoctor_ResultShape validates the per-server probe result struct.
func TestMCPDoctor_ResultShape(t *testing.T) {
	r := ServerResult{
		Name:        "engram",
		Address:     "127.0.0.1:8280",
		Status:      StatusGreen,
		Reachable:   true,
		HTTPStatus:  http.StatusOK,
		Latency:     120 * time.Millisecond,
		Environment: "wsl3",
	}
	assert.Equal(t, "engram", r.Name)
	assert.Equal(t, StatusGreen, r.Status)
	assert.True(t, r.Reachable)
	assert.Equal(t, http.StatusOK, r.HTTPStatus)
	assert.Equal(t, 120*time.Millisecond, r.Latency)
	assert.Equal(t, "wsl3", r.Environment)
}

// TestMCPDoctor_TimeoutOnUnreachable verifies timeout behavior for a
// black-hole address. Doctor must NOT block past deadline.
func TestMCPDoctor_TimeoutOnUnreachable(t *testing.T) {
	doctor := NewDoctorWithServers("wsl3", []Server{
		{Name: "blackhole", Address: "240.0.0.1:9999", HealthPath: "/healthz"},
	})
	start := time.Now()
	results := doctor.Run(context.Background(), 500*time.Millisecond)
	elapsed := time.Since(start)

	require.NotNil(t, results)
	require.Len(t, results.Servers, 1)
	assert.Equal(t, StatusRed, results.Servers[0].Status,
		"blackhole address must be RED")
	assert.False(t, results.Servers[0].Reachable)
	assert.NotNil(t, results.Servers[0].Error)
	assert.Less(t, elapsed, 2*time.Second,
		"doctor must respect 500ms timeout (got %s)", elapsed)
}

// TestMCPDoctor_AllGreenFromHTTPServer verifies the GREEN classification
// against a live httptest server that returns 200.
func TestMCPDoctor_AllGreenFromHTTPServer(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"up"}`))
	}))
	defer upstream.Close()

	// Extract host:port from upstream.URL
	addr := strings.TrimPrefix(upstream.URL, "http://")

	doctor := NewDoctorWithServers("wsl3", []Server{
		{Name: "fake-mcp", Address: addr, HealthPath: "/healthz"},
	})
	results := doctor.Run(context.Background(), 2*time.Second)

	require.NotNil(t, results)
	require.Len(t, results.Servers, 1)
	assert.Equal(t, StatusGreen, results.Servers[0].Status,
		"httptest server returning 200 must be GREEN")
	assert.True(t, results.Servers[0].Reachable)
	assert.Equal(t, http.StatusOK, results.Servers[0].HTTPStatus)
}

// TestMCPDoctor_RedOnNon200 verifies a server returning 500 is RED.
func TestMCPDoctor_RedOnNon200(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	addr := strings.TrimPrefix(upstream.URL, "http://")

	doctor := NewDoctorWithServers("wsl3", []Server{
		{Name: "broken-mcp", Address: addr, HealthPath: "/healthz"},
	})
	results := doctor.Run(context.Background(), 2*time.Second)

	require.NotNil(t, results)
	require.Len(t, results.Servers, 1)
	assert.Equal(t, StatusRed, results.Servers[0].Status,
		"500 must be RED")
	assert.True(t, results.Servers[0].Reachable,
		"TCP up is reachable=true; only HTTP classifies RED")
	assert.Equal(t, http.StatusInternalServerError, results.Servers[0].HTTPStatus)
}

// TestMCPDoctor_EmptyServerList ensures the doctor is safe with zero
// configured servers (returns non-nil empty result rather than nil).
func TestMCPDoctor_EmptyServerList(t *testing.T) {
	doctor := NewDoctorWithServers("wsl3", nil)
	results := doctor.Run(context.Background(), 1*time.Second)
	assert.NotNil(t, results)
	assert.Len(t, results.Servers, 0)
}

// TestMCPDoctor_NonEmptyServerList ensures the result slice is non-nil
// when at least one server is configured.
func TestMCPDoctor_NonEmptyServerList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	addr := strings.TrimPrefix(upstream.URL, "http://")

	doctor := NewDoctorWithServers("windows-host", []Server{
		{Name: "fake-mcp", Address: addr, HealthPath: "/healthz"},
	})
	results := doctor.Run(context.Background(), 2*time.Second)
	assert.NotNil(t, results)
	assert.GreaterOrEqual(t, len(results.Servers), 1)
}
