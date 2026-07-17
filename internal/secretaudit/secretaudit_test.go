package secretaudit_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/secretaudit"
	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

// stubResolver is a minimal Resolver for tests. It records the call and
// returns the configured value or error.
type stubResolver struct {
	gotVault, gotItem, gotField string
	returnValue                 string
	returnErr                   error
}

func (s *stubResolver) ResolveSecret(ctx context.Context, vault, itemUUID, fieldID string) (string, error) {
	s.gotVault = vault
	s.gotItem = itemUUID
	s.gotField = fieldID
	return s.returnValue, s.returnErr
}

func TestWithAudit_PropagatesTenantFromContext(t *testing.T) {
	// Save and restore the global Logger.
	orig := secretaudit.Logger
	defer func() { secretaudit.Logger = orig }()

	var captured struct {
		mu      sync.Mutex
		records []map[string]string
	}
	secretaudit.Logger = func(tenantID, vault, itemUUID, fieldID string) {
		captured.mu.Lock()
		defer captured.mu.Unlock()
		captured.records = append(captured.records, map[string]string{
			"tenant_id": tenantID,
			"vault":     vault,
			"item":      itemUUID,
			"field":     fieldID,
		})
	}

	stub := &stubResolver{returnValue: "secret-value"}
	ctx := tenantid.WithTenantID(context.Background(), "ctx-tenant")

	got, err := secretaudit.WithAudit(ctx, stub, "HelixonSafe", "ilsww3ycbjmtbwra2wmpsfryae", "password")
	if err != nil {
		t.Fatalf("WithAudit err = %v", err)
	}
	if got != "secret-value" {
		t.Fatalf("value = %q; want %q", got, "secret-value")
	}
	if stub.gotVault != "HelixonSafe" || stub.gotItem != "ilsww3ycbjmtbwra2wmpsfryae" || stub.gotField != "password" {
		t.Fatalf("resolver args wrong: vault=%s item=%s field=%s", stub.gotVault, stub.gotItem, stub.gotField)
	}
	if len(captured.records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(captured.records))
	}
	if captured.records[0]["tenant_id"] != "ctx-tenant" {
		t.Fatalf("tenant_id = %q; want %q", captured.records[0]["tenant_id"], "ctx-tenant")
	}
}

func TestWithAudit_FallsBackToEnvWhenNoContext(t *testing.T) {
	orig := secretaudit.Logger
	defer func() { secretaudit.Logger = orig }()

	t.Setenv("HELIXON_TENANT_ID", "env-tenant")

	var captured []string
	secretaudit.Logger = func(tenantID, _, _, _ string) {
		captured = append(captured, tenantID)
	}

	stub := &stubResolver{returnValue: "ok"}
	_, err := secretaudit.WithAudit(context.Background(), stub, "HelixonSafe", "abc", "def")
	if err != nil {
		t.Fatalf("WithAudit err = %v", err)
	}
	if len(captured) != 1 || captured[0] != "env-tenant" {
		t.Fatalf("captured = %v; want [env-tenant]", captured)
	}
}

func TestWithAudit_DefaultsToDefaultWhenNoTenant(t *testing.T) {
	orig := secretaudit.Logger
	defer func() { secretaudit.Logger = orig }()

	t.Setenv("HELIXON_TENANT_ID", "")
	var captured []string
	secretaudit.Logger = func(tenantID, _, _, _ string) {
		captured = append(captured, tenantID)
	}

	stub := &stubResolver{returnValue: "ok"}
	_, err := secretaudit.WithAudit(context.Background(), stub, "HelixonSafe", "abc", "def")
	if err != nil {
		t.Fatalf("WithAudit err = %v", err)
	}
	if len(captured) != 1 || captured[0] != "default" {
		t.Fatalf("captured = %v; want [default]", captured)
	}
}

func TestWithAudit_PropagatesErrorFromResolver(t *testing.T) {
	orig := secretaudit.Logger
	defer func() { secretaudit.Logger = orig }()

	stub := &stubResolver{returnErr: errors.New("vault down")}
	_, err := secretaudit.WithAudit(context.Background(), stub, "HelixonSafe", "abc", "def")
	if err == nil || err.Error() != "vault down" {
		t.Fatalf("err = %v; want %q", err, "vault down")
	}
}

func TestWithAudit_NilLoggerIsSafe(t *testing.T) {
	// Nil Logger should not panic.
	secretaudit.Logger = nil
	stub := &stubResolver{returnValue: "ok"}
	got, err := secretaudit.WithAudit(context.Background(), stub, "HelixonSafe", "abc", "def")
	if err != nil {
		t.Fatalf("WithAudit err = %v", err)
	}
	if got != "ok" {
		t.Fatalf("got = %q; want %q", got, "ok")
	}
}
