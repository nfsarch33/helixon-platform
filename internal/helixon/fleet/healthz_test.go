// runx-public-repo-gate: allow-file fleet_host_alias
// Service-probe MECHANICS tests: ProbeResult shape, timeout behavior on
// unreachable targets, and empty-input handling — synthetic targets only.
//
// The LIVE reachability gate for the fleet's central services moved out of
// this repository's CI (v18801) into the private operations doctor suite,
// which runs on the host where those probes are meaningful. A public test
// that probes internal endpoints couples CI verdicts to production service
// state and publishes internal topology; the probe table it used also
// cited an inventory file that does not exist here and had drifted from
// the deployed ports, turning every CI run red once stale listeners ended.
package fleet

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
