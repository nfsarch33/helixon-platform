// Tests for PilotConfig (v16751-6).
package helixon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPilotConfig_Validate_DisabledAlwaysSafe(t *testing.T) {
	cases := []struct {
		name string
		p    PilotConfig
	}{
		{"zero", PilotConfig{}},
		{"explicit_disabled", PilotConfig{Enabled: false}},
		{"disabled_with_tenant", PilotConfig{Enabled: false, TenantName: "x"}},
		{"disabled_with_zero_ramp", PilotConfig{Enabled: false, RampWindowHours: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(); err != nil {
				t.Errorf("disabled pilot must validate; got %v", err)
			}
		})
	}
}

func TestPilotConfig_Validate_EnabledRequiresTenantAndRamp(t *testing.T) {
	if err := (&PilotConfig{Enabled: true}).Validate(); err == nil {
		t.Error("enabled without tenant should fail Validate")
	}
	if err := (&PilotConfig{Enabled: true, TenantName: "acme", RampWindowHours: 0}).Validate(); err == nil {
		t.Error("enabled with 0 ramp should fail Validate")
	}
	if err := (&PilotConfig{Enabled: true, TenantName: "acme", RampWindowHours: -1}).Validate(); err == nil {
		t.Error("enabled with negative ramp should fail Validate")
	}
}

func TestPilotConfig_Validate_EnabledAcceptsValid(t *testing.T) {
	p := &PilotConfig{Enabled: true, TenantName: "acme-corp", RampWindowHours: 24}
	if err := p.Validate(); err != nil {
		t.Errorf("valid enabled pilot must pass Validate; got %v", err)
	}
}

func TestPilotConfig_RampDuration(t *testing.T) {
	if got := (&PilotConfig{RampWindowHours: 0}).RampDuration(); got != 24*time.Hour {
		t.Errorf("RampDuration with 0 hours = %v; want 24h safe default", got)
	}
	if got := (&PilotConfig{RampWindowHours: 48}).RampDuration(); got != 48*time.Hour {
		t.Errorf("RampDuration with 48 hours = %v; want 48h", got)
	}
}

func TestLoadPilotConfig_DefaultDisabled(t *testing.T) {
	t.Setenv("HELIXON_PILOT_CONFIG", "")
	p, err := LoadPilotConfig()
	if err != nil {
		t.Fatalf("LoadPilotConfig default: %v", err)
	}
	if p.Enabled {
		t.Error("default config should be disabled")
	}
	if p.RampWindowHours != 24 {
		t.Errorf("default RampWindowHours = %d; want 24", p.RampWindowHours)
	}
}

func TestLoadPilotConfig_MissingFileIsSafe(t *testing.T) {
	t.Setenv("HELIXON_PILOT_CONFIG", "/tmp/this-file-does-not-exist-helixon-pilot.yaml")
	p, err := LoadPilotConfig()
	if err != nil {
		t.Fatalf("missing file must not error; got %v", err)
	}
	if p.Enabled {
		t.Error("missing file should produce disabled config")
	}
}

func TestLoadPilotConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pilot.yaml")
	yaml := `enabled: true
tenant_name: beta-pilot-1
ramp_window_hours: 12
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	t.Setenv("HELIXON_PILOT_CONFIG", path)
	p, err := LoadPilotConfig()
	if err != nil {
		t.Fatalf("LoadPilotConfig: %v", err)
	}
	if !p.Enabled {
		t.Error("expected enabled=true")
	}
	if p.TenantName != "beta-pilot-1" {
		t.Errorf("TenantName = %q; want beta-pilot-1", p.TenantName)
	}
	if p.RampWindowHours != 12 {
		t.Errorf("RampWindowHours = %d; want 12", p.RampWindowHours)
	}
}

func TestLoadPilotConfig_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HELIXON_PILOT_CONFIG", path)
	if _, err := LoadPilotConfig(); err == nil {
		t.Error("LoadPilotConfig should fail Validate on enabled-but-no-tenant")
	}
}
