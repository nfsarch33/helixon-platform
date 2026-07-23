// runx-public-repo-gate: allow-file fleet_host_alias,secret_cred_ref
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_PrimaryVendorAndForbidden(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yaml := `
primary_vendor: resend
fallback_vendor: brevo
forbidden_vendors: [smtp2go]
rotate_after_seconds: 60
resend: helixon@resend.dev
brevo: helixon@oztac.com.au
keys:
  - alias: resend-jaslian
    vault: Cursor_IronClaw
    item_id: pwjkp2gii6cnaqwwj4fmesdxd4
    field: api key
    vendor: resend
    status: active
  - alias: smtp2go
    vendor: smtp2go
    status: retired
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.primary != "resend" {
		t.Fatalf("primary: %q", cfg.primary)
	}
	if cfg.fallback != "brevo" {
		t.Fatalf("fallback: %q", cfg.fallback)
	}
	if !cfg.forbidden["smtp2go"] {
		t.Fatalf("smtp2go not forbidden: %v", cfg.forbidden)
	}
	if len(cfg.keys) != 2 {
		t.Fatalf("keys: %d", len(cfg.keys))
	}
	if cfg.keys[0].Alias != "resend-jaslian" || cfg.keys[0].ItemID == "" {
		t.Fatalf("first key bad: %+v", cfg.keys[0])
	}
}

func TestSelectKey_PrefersActive_AndHonorsCooldown(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{
		primary:     "resend",
		fallback:    "brevo",
		forbidden:   map[string]bool{},
		from:        map[string]string{"resend": "helixon@resend.dev"},
		rotateAfter: 60 * 1e9,
		recipients:  parsedRecipients{primary: "jaslian@gmail.com"},
		keys: []vendorKey{
			{Alias: "resend-1", Vendor: "resend", Status: "active"},
			{Alias: "resend-2", Vendor: "resend", Status: "active"},
		},
	}
	got := selectKey(cfg, "resend")
	if got == nil || got.Alias != "resend-1" {
		t.Fatalf("first pick wrong: %+v", got)
	}
	cfg.keys[0].LastUsedAt = "2099-01-01T00:00:00Z"
	got = selectKey(cfg, "resend")
	if got == nil || got.Alias != "resend-2" {
		t.Fatalf("second pick wrong after cooldown: %+v", got)
	}
}

func TestSelectKey_SkipsRetired_AndFallsBack(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{
		primary:     "resend",
		fallback:    "brevo",
		forbidden:   map[string]bool{},
		from:        map[string]string{"brevo": "helixon@oztac.com.au"},
		rotateAfter: 60 * 1e9,
		recipients:  parsedRecipients{primary: "jaslian@gmail.com"},
		keys: []vendorKey{
			{Alias: "smtp2go-1", Vendor: "smtp2go", Status: "retired"},
			{Alias: "brevo-1", Vendor: "brevo", Status: "active"},
		},
	}
	if selectKey(cfg, "smtp2go") != nil {
		t.Fatal("must not pick smtp2go")
	}
	got := selectKey(cfg, "brevo")
	if got == nil || got.Alias != "brevo-1" {
		t.Fatalf("fallback to brevo failed: %+v", got)
	}
}

func TestSelectKey_NoActiveReturnsNil(t *testing.T) {
	t.Parallel()
	cfg := &parsedConfig{
		primary:     "resend",
		fallback:    "brevo",
		forbidden:   map[string]bool{},
		from:        map[string]string{},
		rotateAfter: 60 * 1e9,
		recipients:  parsedRecipients{primary: "jaslian@gmail.com"},
		keys: []vendorKey{
			{Alias: "resend-1", Vendor: "resend", Status: "demoted"},
			{Alias: "brevo-1", Vendor: "brevo", Status: "retired"},
		},
	}
	if selectKey(cfg, "resend") != nil || selectKey(cfg, "brevo") != nil {
		t.Fatal("both should be nil")
	}
}

func TestSplitKV_Basic(t *testing.T) {
	t.Parallel()
	k, v := splitKV("foo: bar baz")
	if k != "foo" || v != "bar baz" {
		t.Fatalf("got %q=%q", k, v)
	}
}

func TestSplitList_Basic(t *testing.T) {
	t.Parallel()
	out := splitList("[a, b, 'c']")
	if len(out) != 3 || out[0] != "a" || out[2] != "c" {
		t.Fatalf("got %v", out)
	}
}
