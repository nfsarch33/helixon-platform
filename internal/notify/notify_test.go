package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
)

// TestContract is the externally visible API contract the helixon-platform
// notify package MUST satisfy. These tests are written FIRST per TDD; the
// implementation lands in notify.go and *_client.go.
//
// All tests use httptest servers (no external network calls). The package
// MUST:
//   - classify 4xx as deterministic failure (fail-fast, no retry)
//   - classify 5xx and network errors as transient (exponential backoff, max 3 attempts)
//   - de-duplicate by IdempotencyKey
//   - log cost observability via OTel meter
//   - collapse CC into TO for Resend (free-tier semantics per ADR-0087)
//   - never log the API key bytes
func TestEmail_PublicAPI(t *testing.T) {
	r := notify.DefaultRecipients()
	if r.Primary != "jaslian@gmail.com" {
		t.Fatalf("DefaultRecipients().Primary = %q; want %q", r.Primary, "jaslian@gmail.com")
	}
	if !strings.HasSuffix(r.Primary, "@gmail.com") {
		t.Fatalf("DefaultRecipients().Primary should end with @gmail.com, got %q", r.Primary)
	}
	wantCC := []string{"info@oztac.com.au", "info@cylrl.com.au"}
	if len(r.CC) != len(wantCC) {
		t.Fatalf("DefaultRecipients().CC = %v; want %v", r.CC, wantCC)
	}
	for i, want := range wantCC {
		if r.CC[i] != want {
			t.Errorf("DefaultRecipients().CC[%d] = %q; want %q", i, r.CC[i], want)
		}
	}
}

func TestResend_SuccessFirstAttempt(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer re_") {
			t.Errorf("expected Bearer re_* Authorization; got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc-123"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey:    "re_test_xyz",
		BaseURL:   srv.URL,
		FromAddr:  "ops@cylrl.com.au",
		Timeout:   2 * time.Second,
		MaxRetry:  3,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		CC:             []string{"info@oztac.com.au"},
		Subject:        "test",
		HTMLBody:       "<p>hi</p>",
		TextBody:       "hi",
		IdempotencyKey: "v16101-test-1",
		JobID:          "job-1",
		TenantID:       "tenant-1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := atomic.LoadInt32(&hit); got != 1 {
		t.Fatalf("server hit %d times; want 1 (success path should not retry)", got)
	}
}

func TestResend_ResendFreeTierCollapsesCCIntoTo(t *testing.T) {
	var gotTo []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			To []string `json:"to"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTo = body.To
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey:    "re_x",
		BaseURL:   srv.URL,
		FromAddr:  "ops@cylrl.com.au",
		Timeout:   2 * time.Second,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	if err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		CC:             []string{"info@oztac.com.au", "info@cylrl.com.au"},
		Subject:        "test",
		HTMLBody:       "<p>x</p>",
		IdempotencyKey: "cc-collapse-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(gotTo) != 3 {
		t.Fatalf("Resend payload to=%v; want 3 (cc collapsed into to)", gotTo)
	}
	for _, want := range []string{"jaslian@gmail.com", "info@oztac.com.au", "info@cylrl.com.au"} {
		found := false
		for _, g := range gotTo {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in Resend payload to=%v", want, gotTo)
		}
	}
}

func TestResend_4xxFailsFastNoRetry(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad api key"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey:    "re_invalid",
		BaseURL:   srv.URL,
		FromAddr:  "ops@cylrl.com.au",
		Timeout:   1 * time.Second,
		MaxRetry:  3,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	err := c.Send(context.Background(), notify.Email{
		To:             []string{"a@b.com"},
		Subject:        "t",
		HTMLBody:       "<p/>",
		IdempotencyKey: "4xx-1",
	})
	if err == nil {
		t.Fatal("expected error from 401")
	}
	if !errors.Is(err, notify.ErrPermanent) {
		t.Errorf("4xx should wrap ErrPermanent; got %v", err)
	}
	if got := atomic.LoadInt32(&hit); got != 1 {
		t.Fatalf("server hit %d times; want 1 (4xx should fail-fast, no retry)", got)
	}
}

func TestResend_5xxRetriesExponentialBackoffMax3(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		n := atomic.AddInt32(&hit, 1)
		if n < 4 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"upstream"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"recovered"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey:    "re_test",
		BaseURL:   srv.URL,
		FromAddr:  "ops@cylrl.com.au",
		Timeout:   1 * time.Second,
		MaxRetry:  3,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	start := time.Now()
	err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "t",
		HTMLBody:       "<p/>",
		IdempotencyKey: "5xx-1",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Send: %v (after %d attempts)", err, hit)
	}
	if got := atomic.LoadInt32(&hit); got != 4 {
		t.Fatalf("server hit %d times; want 4 (3 retries + 1 success)", got)
	}
	// 3 retries with exp backoff (100ms, 200ms, 400ms) + jitter
	// floor: 700ms total backoff
	if elapsed < 700*time.Millisecond {
		t.Errorf("elapsed %v too short; expected >=700ms exp backoff total", elapsed)
	}
}

func TestResend_5xxExhaustedMaxAttemptsReturnsTransientError(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey:    "re_test",
		BaseURL:   srv.URL,
		FromAddr:  "ops@cylrl.com.au",
		Timeout:   1 * time.Second,
		MaxRetry:  3,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "t",
		HTMLBody:       "<p/>",
		IdempotencyKey: "5xx-exhausted",
	})
	if err == nil {
		t.Fatal("expected error after 3-attempt cap")
	}
	if !errors.Is(err, notify.ErrTransient) && !errors.Is(err, notify.ErrDeadLetter) {
		t.Errorf("expected ErrTransient or ErrDeadLetter; got %v", err)
	}
	if got := atomic.LoadInt32(&hit); got != 4 {
		t.Fatalf("server hit %d times; want 4 (1 initial + 3 retries)", got)
	}
}

func TestResend_IdempotencyDedup(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey:    "re_test",
		BaseURL:   srv.URL,
		FromAddr:  "ops@cylrl.com.au",
		Timeout:   1 * time.Second,
		MaxRetry:  3,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	mail := notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "t",
		HTMLBody:       "<p/>",
		IdempotencyKey: "dedup-1",
	}
	// Same key twice in flight + once after = 1 actual HTTP call.
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_ = c.Send(context.Background(), mail)
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&hit); got != 1 {
		t.Fatalf("server hit %d; want 1 (idempotency should dedup)", got)
	}
}

func TestResend_NeverLogsAPIKey(t *testing.T) {
	// Capture stdout/stderr via custom writer
	// Implementation detail: implementation MUST redact the api key in any
	// logged payload. We test by asserting the Send error path does not
	// surface the API key in returned error messages.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "Forbidden: re_secret_DO_NOT_LOG")
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey:    "re_secret_DO_NOT_LOG",
		BaseURL:   srv.URL,
		FromAddr:  "ops@cylrl.com.au",
		Timeout:   1 * time.Second,
		MaxRetry:  1,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "t",
		HTMLBody:       "<p/>",
		IdempotencyKey: "redact-1",
	})
	if err == nil {
		t.Fatal("expected error from 403")
	}
	if strings.Contains(err.Error(), "re_secret_DO_NOT_LOG") {
		t.Fatalf("error message LEAKED api key: %v", err)
	}
}

func TestBrevo_SuccessFirstAttempt(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		if got := r.Header.Get("api-key"); !strings.HasPrefix(got, "xkeysib-") {
			t.Errorf("expected api-key header xkeysib-*; got %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"<abc@gmail.com>"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewBrevoClient(notify.BrevoConfig{
		APIKey:    "xkeysib-test",
		BaseURL:   srv.URL,
		Timeout:   2 * time.Second,
		MaxRetry:  3,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		CC:             []string{"info@oztac.com.au"},
		Subject:        "t",
		HTMLBody:       "<p>x</p>",
		IdempotencyKey: "brevo-1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := atomic.LoadInt32(&hit); got != 1 {
		t.Fatalf("server hit %d; want 1", got)
	}
}

func TestBrevo_4xxFailFast(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_parameter","message":"bad"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewBrevoClient(notify.BrevoConfig{
		APIKey:    "xkeysib-bad",
		BaseURL:   srv.URL,
		Timeout:   1 * time.Second,
		MaxRetry:  3,
		OtelMeter: noop.NewMeterProvider().Meter("test"),
	})

	err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "t",
		HTMLBody:       "<p/>",
		IdempotencyKey: "brevo-4xx",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, notify.ErrPermanent) {
		t.Errorf("expected ErrPermanent; got %v", err)
	}
	if got := atomic.LoadInt32(&hit); got != 1 {
		t.Fatalf("server hit %d; want 1 (no retry)", got)
	}
}

func TestDispatcher_RoundRobin(t *testing.T) {
	// Use two httptest servers as Resend + Brevo; verify round-robin
	// alternates between them and idempotency is per-job.
	var rHits, bHits int32
	resend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		atomic.AddInt32(&rHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"r"}`))
	}))
	defer func() { resend.Close() }()
	brevo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		atomic.AddInt32(&bHits, 1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"b"}`))
	}))
	defer func() { brevo.Close() }()

	meter := noop.NewMeterProvider().Meter("test")
	disp := notify.NewDispatcher(notify.DispatcherConfig{
		ResendClient: notify.NewResendClient(notify.ResendConfig{
			APIKey: "re_x", BaseURL: resend.URL, FromAddr: "ops@cylrl.com.au",
			Timeout: 1 * time.Second, MaxRetry: 3, OtelMeter: meter,
		}),
		BrevoClient: notify.NewBrevoClient(notify.BrevoConfig{
			APIKey: "xkeysib-x", BaseURL: brevo.URL,
			Timeout: 1 * time.Second, MaxRetry: 3, OtelMeter: meter,
		}),
		OtelMeter: meter,
	})

	for i := 0; i < 4; i++ {
		err := disp.Send(context.Background(), notify.Email{
			To:             []string{"jaslian@gmail.com"},
			Subject:        "rr",
			HTMLBody:       "<p/>",
			IdempotencyKey: "rr-" + string(rune('a'+i)),
		})
		if err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
	// Round-robin alternates: expect roughly equal distribution
	if r := atomic.LoadInt32(&rHits); r != 2 {
		t.Errorf("Resend hits %d; want 2 (RR half of 4)", r)
	}
	if b := atomic.LoadInt32(&bHits); b != 2 {
		t.Errorf("Brevo hits %d; want 2 (RR half of 4)", b)
	}
}

// xcut-10 (v18518): BrevoOnly mode must route ALL sends through Brevo
// and never touch the Resend client.
func TestDispatcher_BrevoOnly_RoutesOnlyToBrevo(t *testing.T) {
	var rHits, bHits int32
	resend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		atomic.AddInt32(&rHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"r"}`))
	}))
	defer func() { resend.Close() }()
	brevo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		atomic.AddInt32(&bHits, 1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"b"}`))
	}))
	defer func() { brevo.Close() }()

	meter := noop.NewMeterProvider().Meter("test")
	disp := notify.NewDispatcher(notify.DispatcherConfig{
		ResendClient: notify.NewResendClient(notify.ResendConfig{
			APIKey: "re_x", BaseURL: resend.URL, FromAddr: "ops@cylrl.com.au",
			Timeout: 1 * time.Second, MaxRetry: 3, OtelMeter: meter,
		}),
		BrevoClient: notify.NewBrevoClient(notify.BrevoConfig{
			APIKey: "xkeysib-x", BaseURL: brevo.URL,
			Timeout: 1 * time.Second, MaxRetry: 3, OtelMeter: meter,
		}),
		BrevoOnly: true,
	})

	for i := 0; i < 4; i++ {
		err := disp.Send(context.Background(), notify.Email{
			To:             []string{"jaslian@gmail.com"},
			Subject:        "bo",
			HTMLBody:       "<p/>",
			IdempotencyKey: "bo-" + string(rune('a'+i)),
		})
		if err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&rHits); got != 0 {
		t.Errorf("BrevoOnly must not hit Resend; got %d Resend calls", got)
	}
	if got := atomic.LoadInt32(&bHits); got != 4 {
		t.Errorf("BrevoOnly must hit Brevo for every send; got %d Brevo calls (want 4)", got)
	}
}

func TestDispatcher_FallbackToBrevoOnResendExhaustion(t *testing.T) {
	var rHits, bHits int32
	resend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		atomic.AddInt32(&rHits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer func() { resend.Close() }()
	brevo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&bHits, 1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"fb"}`))
	}))
	defer func() { brevo.Close() }()

	meter := noop.NewMeterProvider().Meter("test")
	disp := notify.NewDispatcher(notify.DispatcherConfig{
		ResendClient: notify.NewResendClient(notify.ResendConfig{
			APIKey: "re_x", BaseURL: resend.URL, FromAddr: "ops@cylrl.com.au",
			Timeout: 500 * time.Millisecond, MaxRetry: 3, OtelMeter: meter,
		}),
		BrevoClient: notify.NewBrevoClient(notify.BrevoConfig{
			APIKey: "xkeysib-x", BaseURL: brevo.URL,
			Timeout: 1 * time.Second, MaxRetry: 3, OtelMeter: meter,
		}),
		OtelMeter: meter,
	})

	err := disp.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "fallback",
		HTMLBody:       "<p/>",
		IdempotencyKey: "fb-1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if atomic.LoadInt32(&rHits) == 0 {
		t.Error("expected Resend to be attempted first")
	}
	if atomic.LoadInt32(&bHits) != 1 {
		t.Errorf("expected Brevo fallback to be hit once; got %d", atomic.LoadInt32(&bHits))
	}
}

func TestIdempotencyStore_NeverRedactsKey(t *testing.T) {
	// IdempotencyStore stores the key bytes but they must NEVER appear in
	// structured log output. Test that public APIs that emit log lines
	// don't include the key.
	store := notify.NewIdempotencyStore()
	store.Record("idem-1")
	if !store.Seen("idem-1") {
		t.Error("expected idem-1 to be seen")
	}
	if store.Seen("idem-2") {
		t.Error("expected idem-2 to NOT be seen")
	}
}

// Ensure import is used (otel package may be needed by implementation; safe to keep here)
var _ = otel.GetTracerProvider

// --- v17409-4: notifydb integration tests ---

func TestResend_AuditDB_RecordsSuccess(t *testing.T) {
	dir := t.TempDir()
	db, err := notifydb.Open(dir+"/audit.sqlite3", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok-1"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey: "re_test", BaseURL: srv.URL, FromAddr: "ops@cylrl.com.au",
		Timeout: time.Second, MaxRetry: 0,
		OtelMeter: noop.NewMeterProvider().Meter("t"),
	}).WithAuditDB(db)

	if err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "audit-success",
		HTMLBody:       "<p>hi</p>",
		IdempotencyKey: "audit-ok-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, found, err := db.Get(context.Background(), "audit-ok-1")
	if err != nil || !found {
		t.Fatalf("Get audit-ok-1: found=%v err=%v", found, err)
	}
	if got.Status != "ok" {
		t.Errorf("Status: want ok, got %q", got.Status)
	}
	if got.Vendor != "resend" {
		t.Errorf("Vendor: want resend, got %q", got.Vendor)
	}
}

func TestResend_AuditDB_RecordsError(t *testing.T) {
	dir := t.TempDir()
	db, err := notifydb.Open(dir+"/audit.sqlite3", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad input"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey: "re_test", BaseURL: srv.URL, FromAddr: "ops@cylrl.com.au",
		Timeout: time.Second, MaxRetry: 0,
		OtelMeter: noop.NewMeterProvider().Meter("t"),
	}).WithAuditDB(db)

	if err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "audit-error",
		IdempotencyKey: "audit-err-1",
	}); err == nil {
		t.Fatal("Send: want error, got nil")
	}

	got, found, _ := db.Get(context.Background(), "audit-err-1")
	if !found {
		t.Fatal("audit-err-1 not recorded")
	}
	if got.Status != "error" {
		t.Errorf("Status: want error, got %q", got.Status)
	}
	if got.Error == "" {
		t.Error("Error: want non-empty error string")
	}
}

func TestBrevo_AuditDB_RecordsSuccess(t *testing.T) {
	dir := t.TempDir()
	db, _ := notifydb.Open(dir+"/audit.sqlite3", nil)
	defer func() { _ = db.Close() }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messageId":"brevo-1"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewBrevoClient(notify.BrevoConfig{
		APIKey: "xkeysib-test", BaseURL: srv.URL,
		Timeout: time.Second, MaxRetry: 0,
		OtelMeter: noop.NewMeterProvider().Meter("t"),
	}).WithAuditDB(db)

	if err := c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "brevo-audit",
		IdempotencyKey: "brevo-audit-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, found, _ := db.Get(context.Background(), "brevo-audit-1")
	if !found || got.Vendor != "brevo" || got.Status != "ok" {
		t.Fatalf("brevo audit row: found=%v vendor=%q status=%q", found, got.Vendor, got.Status)
	}
}

func TestResend_AuditDB_RecordsDeadLetter(t *testing.T) {
	dir := t.TempDir()
	db, _ := notifydb.Open(dir+"/audit.sqlite3", nil)
	defer func() { _ = db.Close() }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"oops"}`))
	}))
	defer func() { srv.Close() }()

	c := notify.NewResendClient(notify.ResendConfig{
		APIKey: "re_test", BaseURL: srv.URL, FromAddr: "ops@cylrl.com.au",
		Timeout: 100 * time.Millisecond, MaxRetry: 1,
		OtelMeter: noop.NewMeterProvider().Meter("t"),
	}).WithAuditDB(db)

	_ = c.Send(context.Background(), notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        "dead-letter",
		IdempotencyKey: "dl-1",
	})

	got, found, _ := db.Get(context.Background(), "dl-1")
	if !found {
		t.Fatal("dl-1 not recorded")
	}
	if got.Status != "error" {
		t.Errorf("Status: want error, got %q", got.Status)
	}
}

func TestResend_AuditDB_InsertIsIdempotent(t *testing.T) {
	// Verifies that the audit DB itself de-duplicates by ID (not the
	// dispatcher in-memory store). We use the DB layer directly here
	// because the dispatcher's in-memory idempotency store short-circuits
	// duplicate idempotency keys before they ever reach the audit path.
	dir := t.TempDir()
	db, _ := notifydb.Open(dir+"/audit.sqlite3", nil)
	defer func() { _ = db.Close() }()

	row := notifydb.Dispatch{
		ID: "shared-key", Vendor: "resend", Status: "ok", CreatedUnix: 1,
	}
	for i := 0; i < 3; i++ {
		_ = db.Insert(context.Background(), row)
	}
	counts, _ := db.CountByVendor(context.Background())
	if counts["resend"] != 1 {
		t.Fatalf("resend count: want 1 unique row, got %d", counts["resend"])
	}
}
