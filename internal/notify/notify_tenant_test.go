package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/tenantid"
	"go.opentelemetry.io/otel/metric/noop"
)

// newTestDispatcher wires a ResendClient + BrevoClient against the
// supplied httptest server URLs and returns a Dispatcher ready for the
// tenant-id tests. The Brevo path is preferred to Resend by default,
// so we point both at the same server (the test captures every
// outbound POST).
func newTestDispatcher(srvURL string) *notify.Dispatcher {
	resend := notify.NewResendClient(notify.ResendConfig{
		APIKey: "re_test", BaseURL: srvURL, FromAddr: "ops@cylrl.com.au",
		Timeout: 1 * time.Second, MaxRetry: 1, OtelMeter: noop.NewMeterProvider().Meter("test"),
	})
	brevo := notify.NewBrevoClient(notify.BrevoConfig{
		APIKey: "test", BaseURL: srvURL,
		Timeout: 1 * time.Second, MaxRetry: 1, OtelMeter: noop.NewMeterProvider().Meter("test"),
	})
	return notify.NewDispatcher(notify.DispatcherConfig{
		ResendClient: resend,
		BrevoClient:  brevo,
		BrevoOnly:    true, // single server URL — Brevo only
	})
}

// TestDispatcher_TenantIDFromContext verifies the Dispatcher picks up the
// per-request tenant id from the context and emits it as the
// `X-Tenant-Id` header in the Resend/Brevo payload.
//
// Per CF-172, when the caller supplies a tenant id via
// `tenantid.WithTenantID`, the notify package must propagate that value
// to the vendor payload — even if the Email.TenantID field is empty.
// This guarantees audit attribution for multi-tenant deployments.
func TestDispatcher_TenantIDFromContext(t *testing.T) {
	var captured atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Store(string(body))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	d := newTestDispatcher(srv.URL)

	ctx := tenantid.WithTenantID(context.Background(), "acme-corp")
	m := notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "test",
		HTMLBody:       "<p>test</p>",
		IdempotencyKey: "tenant-ctx-test-1",
	}
	if err := d.Send(ctx, m); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	raw, _ := captured.Load().(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload not JSON: %v\nraw=%s", err, raw)
	}
	headers, ok := payload["headers"].(map[string]any)
	if !ok {
		t.Fatalf("payload has no headers: %v", payload)
	}
	if got := headers["X-Tenant-Id"]; got != "acme-corp" {
		t.Fatalf("X-Tenant-Id = %v; want %q", got, "acme-corp")
	}
}

// TestDispatcher_TenantIDFieldFallback verifies that when no context
// tenant is present, the Email.TenantID field is used (backward compat
// for single-tenant callers that populate the field directly).
func TestDispatcher_TenantIDFieldFallback(t *testing.T) {
	var captured atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Store(string(body))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	d := newTestDispatcher(srv.URL)

	m := notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "test",
		HTMLBody:       "<p>test</p>",
		IdempotencyKey: "tenant-field-test-1",
		TenantID:       "legacy-tenant",
	}
	if err := d.Send(context.Background(), m); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	raw, _ := captured.Load().(string)
	var payload map[string]any
	_ = json.Unmarshal([]byte(raw), &payload)
	headers := payload["headers"].(map[string]any)
	if got := headers["X-Tenant-Id"]; got != "legacy-tenant" {
		t.Fatalf("X-Tenant-Id = %v; want %q", got, "legacy-tenant")
	}
}

// TestDispatcher_TenantIDDefaultWhenUnset verifies that when no context
// and no Email.TenantID are set, the X-Tenant-Id header falls back to
// "default" (the boot-time fallback). This preserves pre-multitenancy
// deployments.
func TestDispatcher_TenantIDDefaultWhenUnset(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "")
	var captured atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Store(string(body))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	d := newTestDispatcher(srv.URL)

	m := notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "test",
		HTMLBody:       "<p>test</p>",
		IdempotencyKey: "tenant-default-test-1",
	}
	if err := d.Send(context.Background(), m); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	raw, _ := captured.Load().(string)
	var payload map[string]any
	_ = json.Unmarshal([]byte(raw), &payload)
	headers := payload["headers"].(map[string]any)
	if got := headers["X-Tenant-Id"]; got != "default" {
		t.Fatalf("X-Tenant-Id = %v; want %q", got, "default")
	}
}

// TestDispatcher_TenantIDFromEnvVar verifies the boot-time env var
// fallback. When neither context nor Email.TenantID is set, the env
// var `HELIXON_TENANT_ID` is the source of truth.
func TestDispatcher_TenantIDFromEnvVar(t *testing.T) {
	t.Setenv("HELIXON_TENANT_ID", "env-tenant")
	var captured atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Store(string(body))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"test"}`))
	}))
	defer srv.Close()

	d := newTestDispatcher(srv.URL)

	m := notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "test",
		HTMLBody:       "<p>test</p>",
		IdempotencyKey: "tenant-env-test-1",
	}
	if err := d.Send(context.Background(), m); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	raw, _ := captured.Load().(string)
	var payload map[string]any
	_ = json.Unmarshal([]byte(raw), &payload)
	headers := payload["headers"].(map[string]any)
	if got := headers["X-Tenant-Id"]; got != "env-tenant" {
		t.Fatalf("X-Tenant-Id = %v; want %q", got, "env-tenant")
	}
}
