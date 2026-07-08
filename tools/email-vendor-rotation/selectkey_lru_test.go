package main

import (
	"testing"
	"time"
)

// TestSelectKey_TrueLRU pins the v16712 LRU refactor: selectKey picks the
// active key with the oldest LastUsedAt (or empty), not the first-in-config.
//
// Before the refactor: selectKey returned the first active key not in cooldown,
// which made the rotation config-order-based. After the refactor, it should
// iterate ALL active keys and return the one with the oldest LastUsedAt.
func TestSelectKey_TrueLRU(t *testing.T) {
	now := time.Now()
	cfg := &parsedConfig{
		forbidden:   map[string]bool{},
		from:        map[string]string{},
		rotateAfter: 60 * time.Second,
		keys: []vendorKey{
			// Most recently used — should NOT be picked.
			{Alias: "resend-recent", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-1 * time.Minute).UTC().Format(time.RFC3339)},
			// Oldest — should be picked.
			{Alias: "resend-old", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)},
			// In the middle.
			{Alias: "resend-mid", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-5 * time.Minute).UTC().Format(time.RFC3339)},
		},
	}
	pick := selectKey(cfg, "resend")
	if pick == nil {
		t.Fatal("expected a non-nil key")
	}
	if pick.Alias != "resend-old" {
		t.Errorf("expected LRU to pick resend-old (oldest LastUsedAt), got %q", pick.Alias)
	}
}

func TestSelectKey_TrueLRU_NeverUsedBeatsOldest(t *testing.T) {
	now := time.Now()
	cfg := &parsedConfig{
		forbidden:   map[string]bool{},
		from:        map[string]string{},
		rotateAfter: 60 * time.Second,
		keys: []vendorKey{
			{Alias: "resend-old", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)},
			// Never used — should be picked first.
			{Alias: "resend-fresh", Vendor: "resend", Status: "active",
				LastUsedAt: ""},
		},
	}
	pick := selectKey(cfg, "resend")
	if pick == nil {
		t.Fatal("expected a non-nil key")
	}
	if pick.Alias != "resend-fresh" {
		t.Errorf("expected never-used key to win, got %q", pick.Alias)
	}
}

func TestSelectKey_TrueLRU_SkipsCooldown(t *testing.T) {
	now := time.Now()
	cfg := &parsedConfig{
		forbidden:   map[string]bool{},
		from:        map[string]string{},
		rotateAfter: 5 * time.Minute,
		keys: []vendorKey{
			// Within cooldown — should be skipped.
			{Alias: "resend-just-now", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-30 * time.Second).UTC().Format(time.RFC3339)},
			// Outside cooldown — should be picked.
			{Alias: "resend-long-ago", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)},
		},
	}
	pick := selectKey(cfg, "resend")
	if pick == nil {
		t.Fatal("expected a non-nil key")
	}
	if pick.Alias != "resend-long-ago" {
		t.Errorf("expected LRU to skip cooldown key and pick resend-long-ago, got %q", pick.Alias)
	}
}

func TestSelectKey_TrueLRU_RespectsStatus(t *testing.T) {
	now := time.Now()
	cfg := &parsedConfig{
		forbidden:   map[string]bool{},
		from:        map[string]string{},
		rotateAfter: 60 * time.Second,
		keys: []vendorKey{
			{Alias: "resend-active-old", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)},
			{Alias: "resend-standby-old", Vendor: "resend", Status: "standby",
				LastUsedAt: now.Add(-15 * time.Minute).UTC().Format(time.RFC3339)},
		},
	}
	pick := selectKey(cfg, "resend")
	if pick == nil {
		t.Fatal("expected a non-nil key")
	}
	if pick.Alias != "resend-active-old" {
		t.Errorf("expected only active keys considered, got %q", pick.Alias)
	}
}

func TestSelectKey_TrueLRU_EmptyReturnsNil(t *testing.T) {
	cfg := &parsedConfig{
		forbidden:   map[string]bool{},
		from:        map[string]string{},
		rotateAfter: 60 * time.Second,
		keys:        []vendorKey{},
	}
	if pick := selectKey(cfg, "resend"); pick != nil {
		t.Errorf("expected nil for empty keys, got %+v", pick)
	}
}

func TestSelectKey_TrueLRU_AllCooldownReturnsNil(t *testing.T) {
	now := time.Now()
	cfg := &parsedConfig{
		forbidden:   map[string]bool{},
		from:        map[string]string{},
		rotateAfter: 5 * time.Minute,
		keys: []vendorKey{
			{Alias: "resend-just", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-1 * time.Minute).UTC().Format(time.RFC3339)},
			{Alias: "resend-just-2", Vendor: "resend", Status: "active",
				LastUsedAt: now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)},
		},
	}
	if pick := selectKey(cfg, "resend"); pick != nil {
		t.Errorf("expected nil when all keys in cooldown, got %+v", pick)
	}
}

func TestSelectKey_TrueLRU_ForbiddenVendor(t *testing.T) {
	cfg := &parsedConfig{
		forbidden:   map[string]bool{"smtp2go": true},
		from:        map[string]string{},
		rotateAfter: 60 * time.Second,
		keys: []vendorKey{
			{Alias: "smtp2go-1", Vendor: "smtp2go", Status: "active"},
		},
	}
	if pick := selectKey(cfg, "smtp2go"); pick != nil {
		t.Errorf("expected nil for forbidden vendor, got %+v", pick)
	}
}
