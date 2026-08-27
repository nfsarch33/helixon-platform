// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package alertnotifier

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
)

// fakeResendKey is a deliberately low-entropy, obviously-synthetic stand-in
// that still carries the real "re_" prefix, so the redaction path under
// test is the same one a production key would take.
const fakeResendKey = "re_NOTAREALKEY_alertnotifier000000"

// resendStub serves the vendor endpoint, counts attempts, and echoes the
// bearer token back in its error body — which is exactly how a chatty
// vendor leaks a credential into a log.
func resendStub(t *testing.T, status int, attempts *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		auth := r.Header.Get("Authorization")
		w.WriteHeader(status)
		//nolint:gosec // G705: echoing the credential back is precisely what this stub exists to simulate
		_, _ = w.Write([]byte(`{"message":"rejected request with ` + auth + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newResendSender(t *testing.T, baseURL string, maxRetry int) *notify.ResendClient {
	t.Helper()
	return notify.NewResendClient(notify.ResendConfig{
		APIKey:   fakeResendKey,
		BaseURL:  baseURL,
		FromAddr: DefaultFrom,
		Timeout:  5 * time.Second,
		MaxRetry: maxRetry,
	})
}

func TestVendorErrorClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		status       int
		maxRetry     int
		wantAttempts int32
		wantErr      error
		reason       string
	}{
		{
			name: "4xx is permanent and never retried", status: http.StatusUnprocessableEntity,
			maxRetry: 3, wantAttempts: 1, wantErr: notify.ErrPermanent,
			reason: "an unverified sender or a bad recipient will fail identically forever",
		},
		{
			name: "403 unverified domain is permanent", status: http.StatusForbidden,
			maxRetry: 3, wantAttempts: 1, wantErr: notify.ErrPermanent,
			reason: "this is the live failure for ops@cylrl.com.au",
		},
		{
			name: "401 restricted key is permanent", status: http.StatusUnauthorized,
			maxRetry: 3, wantAttempts: 1, wantErr: notify.ErrPermanent,
			reason: "a send-only key on the wrong endpoint must not be retried",
		},
		{
			name: "5xx is transient and retried within a bounded budget", status: http.StatusBadGateway,
			maxRetry: 1, wantAttempts: 2, wantErr: notify.ErrDeadLetter,
			reason: "retries must be bounded by MaxRetry, never unbounded",
		},
		{
			name: "503 exhausts the budget then dead-letters", status: http.StatusServiceUnavailable,
			maxRetry: 2, wantAttempts: 3, wantErr: notify.ErrDeadLetter,
			reason: "the caller must be told delivery failed, not left waiting",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var attempts atomic.Int32
			srv := resendStub(t, tc.status, &attempts)
			sender := newResendSender(t, srv.URL, tc.maxRetry)

			err := sender.Send(context.Background(), notify.Email{
				To:             []string{DefaultTo},
				Subject:        "[HELIXON] test",
				TextBody:       "body",
				IdempotencyKey: "alert-notifier-test",
			})
			if err == nil {
				t.Fatalf("expected an error (%s)", tc.reason)
			}
			if !isErr(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v (%s)", err, tc.wantErr, tc.reason)
			}
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d (%s)", got, tc.wantAttempts, tc.reason)
			}
		})
	}
}

// isErr wraps errors.Is so the table can stay declarative.
func isErr(err, target error) bool {
	for e := err; e != nil; {
		if e == target { //nolint:errorlint // deliberate identity check inside the unwrap walk
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// TestAPIKeyNeverEscapes drives a full Run against a vendor that echoes the
// bearer token back, then asserts the key appears in no error string, no
// audit line, no rendered output, and no file this component wrote.
func TestAPIKeyNeverEscapes(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	vendor := resendStub(t, http.StatusUnprocessableEntity, &attempts)

	am := newAMStub(t, liveShapedAlerts)

	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), newResendSender(t, vendor.URL, 1), now)

	_, err := Run(context.Background(), h.cfg)
	if err == nil {
		t.Fatal("expected the vendor rejection to surface as an error")
	}
	if attempts.Load() == 0 {
		t.Fatal("the vendor was never called; the redaction path was not exercised")
	}

	surfaces := map[string]string{
		"error string": err.Error(),
		"stdout audit": h.stdout.String(),
		"stderr":       h.stderr.String(),
	}
	for name, s := range surfaces {
		if strings.Contains(s, fakeResendKey) {
			t.Errorf("API key leaked into %s: %s", name, s)
		}
	}
	// The redaction must be positive proof, not an accident of an empty
	// error: the sanitized marker has to be there.
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("expected the echoed credential to be redacted, got: %v", err)
	}

	// Nothing this run wrote to disk may contain the key either.
	walkErr := filepath.WalkDir(h.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path) //nolint:gosec // G304: test-owned temp tree
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), fakeResendKey) {
			t.Errorf("API key leaked into file %s", path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk temp dir: %v", walkErr)
	}
}

// TestAuditRecordCarriesNoBodyOrCredential guards the audit line's shape:
// it may carry counts and identifiers, never the message body and never a
// secret-bearing field.
func TestAuditRecordCarriesNoBodyOrCredential(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), &fakeSender{}, now)

	if _, err := Run(context.Background(), h.cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec := h.auditRecord(t)
	for _, banned := range []string{"api_key", "apikey", "token", "authorization", "text_body", "html_body"} {
		if _, present := rec[banned]; present {
			t.Errorf("audit record must not carry a %q field: %v", banned, rec)
		}
	}
	for _, required := range []string{"idempotency_key", "active_alerts", "new_firing", "resolved", "sent", "textfile_path"} {
		if _, present := rec[required]; !present {
			t.Errorf("audit record missing %q: %v", required, rec)
		}
	}
	if strings.Contains(h.stdout.String(), "TailscaleNodeDown") {
		t.Error("the audit line must not embed the digest body")
	}
}
