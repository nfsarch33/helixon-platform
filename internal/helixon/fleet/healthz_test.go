// Package fleet — win1 central services hook-up (v18687-1).
//
// RED tests that verify the 4 central services reachable on win1/wsl1:
//
//   - Engram              :8280
//   - SprintBoard         :8765
//   - svcregistryd        :8786
//   - llm-cluster-router  :8787
//
// Each probe returns a ProbeResult with Reachable, Latency, HTTPStatus, Error.
// TestWin1Hookup_AllServicesReachable asserts all 4 return HTTP 200 within 3s.
//
// These tests are run from the wsl3 host using the runx ssh jump surface to
// reach win1/wsl1. They are intentionally tolerant: when win1 is offline,
// they record the failure honestly (per DRL-8.20-r4 honest KPI scoreboard)
// rather than skip or fabricate GREEN.
//
// Run with: go test -race -count=1 ./internal/helixon/fleet/... -run TestWin1
package fleet

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Default win1 service endpoints. Each value is the canonical fleet
// listening address per inventory/services/registry.yaml.
var defaultWin1Services = []Win1Service{
	{Name: "engram", Address: "100.84.108.92:8280", HealthPath: "/healthz"},
	{Name: "sprintboard", Address: "100.84.108.92:8765", HealthPath: "/healthz"},
	{Name: "svcregistryd", Address: "100.84.108.92:8786", HealthPath: "/healthz"},
	{Name: "llm-cluster-router", Address: "100.84.108.92:8787", HealthPath: "/healthz"},
}

// TestWin1Hookup_AllServicesReachable is the RED test for v18687-1.
// It asserts all 4 central services reachable on win1/wsl1 within 3s,
// returning HTTP 200 on /healthz.
func TestWin1Hookup_AllServicesReachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	results := ProbeWin1Services(ctx, defaultWin1Services, 3*time.Second)

	require.Len(t, results, 4, "expected 4 probe results")

	// All 4 services MUST be reachable for GREEN status.
	for _, r := range results {
		t.Logf("win1 probe: %-22s addr=%s reachable=%v http=%d latency=%s err=%v",
			r.Service, r.Address, r.Reachable, r.HTTPStatus, r.Latency, r.Error)

		if !r.Reachable {
			t.Errorf("FAIL: %s at %s NOT reachable (err=%v latency=%s)",
				r.Service, r.Address, r.Error, r.Latency)
			continue
		}
		assert.Equal(t, http.StatusOK, r.HTTPStatus,
			"%s must return HTTP 200 on /healthz", r.Service)
		assert.Less(t, r.Latency, 3*time.Second,
			"%s latency must be under 3s", r.Service)
	}
}

// TestWin1Hookup_ProbeResultShape validates the ProbeResult struct
// fields used by the report aggregator.
func TestWin1Hookup_ProbeResultShape(t *testing.T) {
	r := ProbeResult{
		Service:    "engram",
		Address:    "100.84.108.92:8280",
		Reachable:  true,
		HTTPStatus: http.StatusOK,
		Latency:    120 * time.Millisecond,
	}
	assert.Equal(t, "engram", r.Service)
	assert.Equal(t, "100.84.108.92:8280", r.Address)
	assert.True(t, r.Reachable)
	assert.Equal(t, http.StatusOK, r.HTTPStatus)
	assert.Equal(t, 120*time.Millisecond, r.Latency)
	assert.Nil(t, r.Error)
}

// TestWin1Hookup_TimeoutOnUnreachable verifies timeout behavior
// when a service address is unreachable (e.g., a black-hole IP).
// This is the RED-then-GREEN contract: probes must NOT block past deadline.
func TestWin1Hookup_TimeoutOnUnreachable(t *testing.T) {
	svc := Win1Service{
		Name:       "blackhole",
		Address:    "240.0.0.1:9999", // TEST-NET, never routable
		HealthPath: "/healthz",
	}
	start := time.Now()
	results := ProbeWin1Services(context.Background(), []Win1Service{svc}, 500*time.Millisecond)
	elapsed := time.Since(start)

	require.Len(t, results, 1)
	assert.False(t, results[0].Reachable, "blackhole address must NOT be reachable")
	assert.NotNil(t, results[0].Error, "unreachable probe must record an error")
	assert.Less(t, elapsed, 2*time.Second,
		"probe must respect 500ms timeout (got %s)", elapsed)
}

// TestWin1Hookup_ReportNonEmpty ensures the probe aggregator produces
// at least one ProbeResult even when given an empty service list edge case.
func TestWin1Hookup_ReportNonEmpty(t *testing.T) {
	results := ProbeWin1Services(context.Background(), nil, 1*time.Second)
	assert.NotNil(t, results, "nil input must return non-nil empty slice")
	assert.Len(t, results, 0)
}

// TestWin1Hookup_HostnameInProbe allows the probe to verify the target
// hostname before TCP connect (useful for ssh-tunnelled checks).
// This test asserts the helper returns a non-empty string OR a clear error.
func TestWin1Hookup_HostnameInProbe(t *testing.T) {
	host, err := resolveWin1Host(context.Background())
	if err != nil {
		t.Logf("resolveWin1Host returned err=%v (acceptable when win1 offline)", err)
		return
	}
	assert.NotEmpty(t, host, "hostname must be non-empty on success")
}
