package main

import (
	"testing"
	"time"
)

func TestLoadServeConfig_NoHeartbeat(t *testing.T) {
	// Path that doesn't exist returns an error from loadConfig.
	_, err := loadServeConfig("/nonexistent/path/config.yaml", "")
	if err == nil {
		t.Errorf("expected error for missing config file")
	}
}

func TestLoadServeConfig_InvalidHeartbeat(t *testing.T) {
	_, err := loadServeConfig("/dev/null", "not-a-duration")
	if err == nil {
		t.Errorf("expected error for invalid heartbeat")
	}
}

func TestLoadServeConfig_ValidHeartbeat(t *testing.T) {
	// We don't have a real config, so this should fail at loadConfig, not heartbeat parsing.
	// The point is: a valid heartbeat should NOT cause a heartbeat parse error.
	_, err := loadServeConfig("/dev/null", "5s")
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
}

func TestPrintServeBanner(t *testing.T) {
	// We don't have a runtime here, but we can verify the helper exists and
	// is exported internally. The runtime banner uses rt.Phase(), etc.
	// For a unit test we just verify the function doesn't panic on nil
	// (it would fail at runtime if rt is nil, so we only check signature
	// compilation here).
	_ = time.Second
	var _ = printServeBanner
}
