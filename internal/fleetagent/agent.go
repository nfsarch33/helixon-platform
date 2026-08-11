// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
// Package fleetagent is the v0.5.0 implementation of the Helixon fleet
// remote-control daemon + installer. Replaces the shell-based v0.1.0
// installer (scripts/fleet/installer/install.sh).
//
// Layout:
//
//	Install()  - one-shot, idempotent; writes host key, config, env file.
//	Serve()    - long-lived: heartbeat loop + HTTP control server.
//	Doctor()   - delegates to scripts/fleet/installer/doctor.sh.
//	Register() - announces this node to svcregistryd.
//
// Design constraints (per harness-engineering-defaults.mdc):
//   - All goroutines bound by context cancellation; no leaks.
//   - Bounded backoff on upstream errors.
//   - Real time used only via the injected Clock interface.
//   - Idempotent install: re-running is safe; existing host key is preserved.
package fleetagent

import (
	"time"
)

// Version is the agent version, surfaced via the version subcommand and the
// HTTP /version endpoint. Bumped on incompatible protocol changes.
const Version = "0.5.0-dev"

// Defaults - overridable via flags in cmd/fleet-agent/main.go.
const (
	// DefaultConfigPath is the per-user agent.toml location.
	// On Windows the installer rewrites this to %ProgramData%\Helixon\agent.toml.
	DefaultConfigPath = "/etc/helixon/agent.toml"

	// DefaultRegistryURL is the svcregistryd base URL.
	DefaultRegistryURL = "http://127.0.0.1:7777"

	// DefaultHTTPAddr is the control plane listen address.
	DefaultHTTPAddr = ":8686"

	// HeartbeatInterval is the period between heartbeats to svcregistryd.
	HeartbeatInterval = 30 * time.Second

	// HeartbeatTimeout caps a single heartbeat HTTP request.
	HeartbeatTimeout = 5 * time.Second

	// MaxBackoff caps the heartbeat backoff at 5 minutes so transient
	// registry outages do not permanently silence the agent.
	MaxBackoff = 5 * time.Minute

	// InitialBackoff is the first sleep after a heartbeat error.
	InitialBackoff = 5 * time.Second
)

// Clock is the minimal clock interface required by Serve / Heartbeat.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is the minimal timer interface required by Serve / Heartbeat.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// RealClock returns time.Now() and time.NewTimer.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time { return time.Now() }

// NewTimer returns time.NewTimer(d).
func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

// NodeID returns the platform node identifier. Falls back to hostname.
func NodeID() string {
	h, err := hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}
