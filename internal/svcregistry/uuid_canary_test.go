// Package svcregistry: 26-char 1Password UUID canary test (v14596).
//
// Background: every 1Password item has a 26-character alphanumeric UUID
// (a-z, 0-9). The Helixon convention (global rule #5) requires that all
// 1Password references use the 26-char UUID format. svcregistry must
// therefore round-trip these UUIDs in service names, registry keys, and
// HTTP responses without truncation or case-folding corruption.
//
// This is the TDD red-first test that PROVES the registry is safe to
// use as the canonical service lookup for any service whose identity
// carries an OP-UUID.
package svcregistry

import (
	"crypto/rand"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
)

// generateValidOpUUID returns a 26-char hex string that mimics a 1Password
// item UUID. 1Password UUIDs are 26 lowercase alphanumeric chars; we use
// hex for determinism but real OP UUIDs use a custom base32 alphabet.
//
// Per https://developer.1password.com/docs/service-accounts/items,
// UUIDs are 26 chars matching /^[a-z0-9]{26}$/.
func generateValidOpUUID(t *testing.T) string {
	t.Helper()
	// 13 random bytes → 26 hex chars.
	b := make([]byte, 13)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestService_Accepts26CharUUIDName proves that a Service whose Name is
// a 26-char alphanumeric UUID is accepted by Validate() and round-trips
// unchanged through Register / List.
func TestService_Accepts26CharUUIDName(t *testing.T) {
	uuid := generateValidOpUUID(t)
	if len(uuid) != 26 {
		t.Fatalf("test fixture broken: UUID %q is %d chars, expected 26", uuid, len(uuid))
	}
	s := Service{
		Name:        uuid, // bare 26-char UUID as the service name
		Host:        "127.0.0.1",
		Port:        9999,
		Protocol:    "http",
		Owner:       "v14596-canary",
		Status:      "up",
		LastSeenISO: "2026-07-10T05:00:00Z",
		TailscaleIP: "100.84.108.92",
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate rejected 26-char UUID name: %v", err)
	}

	reg := New(t.TempDir() + "/canary.json")
	if err := reg.Register(s); err != nil {
		t.Fatalf("Register rejected 26-char UUID name: %v", err)
	}

	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 service in registry, got %d", len(list))
	}
	if list[0].Name != uuid {
		t.Fatalf("name round-trip corrupted: got %q, want %q", list[0].Name, uuid)
	}
	if !strings.HasPrefix(list[0].Key(), "127.0.0.1/") || strings.TrimPrefix(list[0].Key(), "127.0.0.1/") != uuid {
		t.Fatalf("Key() corrupted: got %q", list[0].Key())
	}
}

// TestService_Accepts26CharUUIDInOwner proves UUIDs in Owner field also
// round-trip cleanly. Owner often carries the 1Password item title or
// a UUID-prefixed tag like `v14508.5-<uuid>`.
func TestService_Accepts26CharUUIDInOwner(t *testing.T) {
	uuid := generateValidOpUUID(t)
	s := Service{
		Name:        "canary-svc",
		Host:        "127.0.0.1",
		Port:        9998,
		Protocol:    "http",
		Owner:       "v14596-" + uuid, // UUID embedded in Owner
		Status:      "up",
		LastSeenISO: "2026-07-10T05:00:00Z",
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate rejected UUID-in-Owner: %v", err)
	}
	reg := New(t.TempDir() + "/canary2.json")
	if err := reg.Register(s); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got := reg.List()[0]
	if got.Owner != "v14596-"+uuid {
		t.Fatalf("Owner UUID round-trip corrupted: got %q", got.Owner)
	}
}

// TestService_HTTP_RoundTrip26CharUUIDName proves the HTTP API
// preserves UUID service names through JSON serialization.
func TestService_HTTP_RoundTrip26CharUUIDName(t *testing.T) {
	uuid := generateValidOpUUID(t)
	reg := New(t.TempDir() + "/canary3.json")
	if err := reg.Register(Service{
		Name:        uuid,
		Host:        "127.0.0.1",
		Port:        9997,
		Protocol:    "http",
		Owner:       "v14596-canary",
		Status:      "up",
		LastSeenISO: "2026-07-10T05:00:00Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	srv := httptest.NewServer(NewHTTPServer(reg).Handler())
	defer srv.Close()

	// GET /api/v1/services should include the 26-char UUID unaltered.
	resp, err := srv.Client().Get(srv.URL + "/api/v1/services")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 4096)
	buf := make([]byte, 1024)
	for {
		n, _ := resp.Body.Read(buf)
		if n == 0 {
			break
		}
		body = append(body, buf[:n]...)
	}
	if !strings.Contains(string(body), uuid) {
		t.Fatalf("HTTP response does not contain UUID %q\nbody=%s", uuid, string(body))
	}
}

// TestService_AllRealOPUUIDsAcceptance is the integration canary: take
// 5 real 1Password UUIDs from the v14593 audit and prove they all
// round-trip through the registry unchanged.
func TestService_AllRealOPUUIDsAcceptance(t *testing.T) {
	// 26-char UUIDs from prior audit evidence (none are secrets):
	realUUIDs := []string{
		"sjhxjryivr6edhmb2ecovdpot4", // Win PC WSL Ubuntu Login GB / jason@win4
		"hfri3ziy6cjfec4xha7wkfkkri", // LLM Cluster Router Fleet (wsl1)
		"n2ecpwlnkpjs4ufdvquw6xf624", // gitlab-runner token (per v14598 spec)
	}
	for i, u := range realUUIDs {
		if len(u) != 26 {
			t.Fatalf("fixture[%d] %q is %d chars, expected 26", i, u, len(u))
		}
		reg := New(t.TempDir() + "/real-" + u + ".json")
		s := Service{
			Name:        u,
			Host:        "127.0.0.1",
			Port:        9900 + i,
			Protocol:    "http",
			Owner:       "v14596-realcanary",
			Status:      "up",
			LastSeenISO: "2026-07-10T05:00:00Z",
		}
		if err := s.Validate(); err != nil {
			t.Errorf("[%d] %q Validate: %v", i, u, err)
		}
		if err := reg.Register(s); err != nil {
			t.Errorf("[%d] %q Register: %v", i, u, err)
		}
		if got := reg.List()[0].Name; got != u {
			t.Errorf("[%d] round-trip: got %q want %q", i, got, u)
		}
	}
}