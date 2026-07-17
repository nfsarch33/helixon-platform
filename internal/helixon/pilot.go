// Package helixon pilot configuration. v16751-6 introduces
// HelixonPilotConfig + 3 TDD tests in pilot_test.go.
//
// Purpose: enable operator-gated pilot-customer wiring without
// requiring code changes. The pilot config is read from a YAML file
// pointed at by HELIXON_PILOT_CONFIG (env) and is OPTIONAL — when the
// env is unset, the platform runs in non-pilot mode (default).
//
// Operator-gated fields (per CF-2026-0708-012):
//   - TenantName: real customer name (3 design-partner pilots)
//   - RampWindowHours: gradual enablement window (24h default)
//   - Enabled: master switch (false = pilot disabled)
//
// Safety invariants enforced by tests:
//   - Disabled configs are inert (no side effects)
//   - Enabled configs require TenantName + non-zero RampWindowHours
//   - Validate() returns a non-nil error on misconfiguration
package helixon

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// PilotDefaults are the zero-value defaults applied when a field is
// absent from the YAML. Operators can override via HELIXON_PILOT_CONFIG
// or by editing the on-disk YAML directly.
var PilotDefaults = PilotConfig{
	Enabled:         false,
	TenantName:      "",
	RampWindowHours: 24,
}

// PilotConfig is the wire shape for v16751+ pilot customer wiring.
// Tests in pilot_test.go enforce the contract.
type PilotConfig struct {
	// Enabled is the master switch. When false, the platform runs in
	// default (non-pilot) mode regardless of other fields.
	Enabled bool `yaml:"enabled"`

	// TenantName is the customer identifier (e.g. "acme-corp").
	// Required when Enabled is true; Validate fails otherwise.
	TenantName string `yaml:"tenant_name"`

	// RampWindowHours is the gradual enablement window. Default 24h;
	// must be > 0 when Enabled is true.
	RampWindowHours int `yaml:"ramp_window_hours"`
}

// Validate enforces the safety invariants. Returns nil iff the config
// is safe to apply.
//
// Behaviour:
//   - Disabled configs always validate as safe (no side effects).
//   - Enabled configs require TenantName != "".
//   - Enabled configs require RampWindowHours > 0.
func (p *PilotConfig) Validate() error {
	if !p.Enabled {
		return nil
	}
	if p.TenantName == "" {
		return errors.New("pilot: enabled=true requires tenant_name")
	}
	if p.RampWindowHours <= 0 {
		return fmt.Errorf("pilot: enabled=true requires ramp_window_hours > 0 (got %d)", p.RampWindowHours)
	}
	return nil
}

// RampDuration returns the time.Duration form of RampWindowHours. If
// RampWindowHours is <= 0, returns 24h as a safe default so the caller
// never has to nil-check the duration.
func (p *PilotConfig) RampDuration() time.Duration {
	if p.RampWindowHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(p.RampWindowHours) * time.Hour
}

// LoadPilotConfig reads the pilot YAML from the path in HELIXON_PILOT_CONFIG
// (env). When the env is unset or the file does not exist, returns the
// zero-value PilotDefaults (pilot disabled). Returns a wrapped error on
// YAML parse failures or Validate() violations.
//
// Calling LoadPilotConfig has no side effects beyond reading the file.
// It is safe to call on every startup.
func LoadPilotConfig() (*PilotConfig, error) {
	path := os.Getenv("HELIXON_PILOT_CONFIG")
	if path == "" {
		// Default to disabled; safe no-op.
		p := PilotDefaults
		return &p, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		if os.IsNotExist(err) {
			// File missing → treat as disabled (operator-gated default).
			p := PilotDefaults
			return &p, nil
		}
		return nil, fmt.Errorf("pilot: read %s: %w", path, err)
	}
	var p PilotConfig
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("pilot: parse %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("pilot: validate %s: %w", path, err)
	}
	return &p, nil
}
