package main

import (
	"testing"
	"time"
)

// v16716-2 RED tests for the loadConfig refactor.
// loadConfig (CC=33) is decomposed into 5 single-responsibility helpers,
// each with cyclomatic complexity <= 5.

func TestParseKeyListItem_AliasStart(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{}
	k, ok := parseKeyListItem(cfg, "- alias: foo", nil)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if k == nil || k.Alias != "foo" {
		t.Fatalf("got %+v", k)
	}
}

func TestParseKeyListItem_NestedField(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{}
	current := &vendorKey{Alias: "k1"}
	k, ok := parseKeyListItem(cfg, "  vendor: resend", current)
	if !ok || k == nil {
		t.Fatal("expected ok=true and current preserved")
	}
	if current.Vendor != "resend" {
		t.Fatalf("vendor: %q", current.Vendor)
	}
}

func TestParseKeyListItem_NestedDailyQuotaIgnored(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{}
	current := &vendorKey{Alias: "k1"}
	_, _ = parseKeyListItem(cfg, "  daily_quota: 100", current)
	applyKeyField(current, "daily_quota", "100")
	if current.Vendor != "" || current.Status != "" {
		t.Fatalf("current should be unchanged: %+v", current)
	}
}

func TestParseTopLevel_PrimaryVendor(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{keys: []vendorKey{}}
	parseTopLevel(cfg, "primary_vendor: resend", nil)
	if cfg.primary != "resend" {
		t.Fatalf("primary: %q", cfg.primary)
	}
}

func TestParseTopLevel_RotateAfterSeconds(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{keys: []vendorKey{}}
	parseTopLevel(cfg, "rotate_after_seconds: 120", nil)
	if cfg.rotateAfter != 120*time.Second {
		t.Fatalf("rotateAfter: %v", cfg.rotateAfter)
	}
}

func TestParseTopLevel_ForbiddenList(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{keys: []vendorKey{}}
	parseTopLevel(cfg, "forbidden_vendors: [a, b]", nil)
	if !cfg.forbidden["a"] || !cfg.forbidden["b"] {
		t.Fatalf("forbidden: %v", cfg.forbidden)
	}
}

func TestParseTopLevel_VendorFromAddress(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{keys: []vendorKey{}, from: map[string]string{}}
	parseTopLevel(cfg, "resend: hello@resend.dev", nil)
	if cfg.from["resend"] != "hello@resend.dev" {
		t.Fatalf("from: %v", cfg.from)
	}
}

func TestParseTopLevel_Smtp2GoFromAddress(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{keys: []vendorKey{}, from: map[string]string{}}
	parseTopLevel(cfg, "smtp2go: legacy@smtp2go.com", nil)
	if cfg.from["smtp2go"] != "legacy@smtp2go.com" {
		t.Fatalf("from: %v", cfg.from)
	}
}

func TestParseTopLevel_PrimaryRecipient(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{keys: []vendorKey{}}
	parseTopLevel(cfg, "primary: someone@example.com", nil)
	if cfg.recipients.primary != "someone@example.com" {
		t.Fatalf("primary recipient: %q", cfg.recipients.primary)
	}
}

func TestFinalizeConfig_DefaultsApplied(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{}
	finalizeConfig(cfg)
	if cfg.recipients.primary != "jaslian@gmail.com" {
		t.Fatalf("default recipient: %q", cfg.recipients.primary)
	}
	if cfg.rotateAfter != 60*time.Second {
		t.Fatalf("default rotateAfter: %v", cfg.rotateAfter)
	}
}

func TestParseLines_Integrated(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{
		keys: []vendorKey{},
		from: map[string]string{},
	}
	lines := []string{
		"primary_vendor: resend",
		"fallback_vendor: brevo",
		"forbidden_vendors: [smtp2go]",
		"rotate_after_seconds: 60",
		"resend: helixon@resend.dev",
		"keys:",
		"  - alias: resend-jaslian",
		"    vendor: resend",
		"    status: active",
	}
	var current *vendorKey
	for _, line := range lines {
		parseLines(cfg, line, &current)
	}
	if current != nil {
		cfg.keys = append(cfg.keys, *current)
	}
	finalizeConfig(cfg)
	if cfg.primary != "resend" {
		t.Fatalf("primary: %q", cfg.primary)
	}
	if !cfg.forbidden["smtp2go"] {
		t.Fatal("smtp2go not forbidden")
	}
	if cfg.from["resend"] != "helixon@resend.dev" {
		t.Fatalf("from: %v", cfg.from)
	}
	if len(cfg.keys) != 1 || cfg.keys[0].Alias != "resend-jaslian" {
		t.Fatalf("keys: %+v", cfg.keys)
	}
	if cfg.recipients.primary != "jaslian@gmail.com" {
		t.Fatalf("default recipient: %q", cfg.recipients.primary)
	}
}
