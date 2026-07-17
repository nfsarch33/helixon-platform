// Package notify — RotatingSender tests (v17607-6c).
package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

//nolint:unparam // vendor string is parameterised to keep the test flexible for multi-vendor assertions in future tests.
func newStubClient(vendor string, calls *atomic.Int32) Client {
	return &stubClient{vendor: vendor, calls: calls}
}

type stubClient struct {
	vendor string
	calls  *atomic.Int32
}

func (s *stubClient) Vendor() string { return s.vendor }
func (s *stubClient) Send(_ context.Context, _ Email) error {
	s.calls.Add(1)
	return nil
}

func openTestDB(t *testing.T) *notifydb.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := notifydb.Open(filepath.Join(dir, "test.sqlite3"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// xcut-10 (v18518): when Resend is unverified (CF-105), Brevo-only mode
// must skip all Resend clients and pick among Brevo keys only. The mode
// is set at construction time via NewRotatingSenderWithMode.
func TestRotatingSender_BrevoOnly_SkipsResend(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Pre-record Resend keys to look older than Brevo — without the mode
	// the LRU pick would otherwise select Resend.
	now := time.Now().Unix()
	if err := db.RecordKeyUse(ctx, notifydb.KeyUse{Vendor: "resend", KeyID: "r1", LastUsedUnix: now - 1000}); err != nil {
		t.Fatalf("record r1: %v", err)
	}
	if err := db.RecordKeyUse(ctx, notifydb.KeyUse{Vendor: "resend", KeyID: "r2", LastUsedUnix: now - 999}); err != nil {
		t.Fatalf("record r2: %v", err)
	}
	if err := db.RecordKeyUse(ctx, notifydb.KeyUse{Vendor: "brevo", KeyID: "b1", LastUsedUnix: now - 10}); err != nil {
		t.Fatalf("record b1: %v", err)
	}

	var r1Calls, r2Calls, b1Calls atomic.Int32
	rs, err := NewRotatingSenderWithMode(db,
		[]Client{
			newStubClient("resend", &r1Calls),
			newStubClient("resend", &r2Calls),
			newStubClient("brevo", &b1Calls),
		},
		[]string{"r1", "r2", "b1"},
		RotatingSenderModeBrevoOnly)
	if err != nil {
		t.Fatalf("NewRotatingSenderWithMode: %v", err)
	}

	if err := rs.Send(ctx, Email{
		To:             []string{"jaslian@gmail.com"},
		IdempotencyKey: "brevo-only-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if b1Calls.Load() != 1 {
		t.Errorf("expected Brevo client to be called once; got b1=%d r1=%d r2=%d",
			b1Calls.Load(), r1Calls.Load(), r2Calls.Load())
	}
	if r1Calls.Load() != 0 || r2Calls.Load() != 0 {
		t.Errorf("expected NO Resend calls in BrevoOnly mode; got r1=%d r2=%d",
			r1Calls.Load(), r2Calls.Load())
	}
}

func TestRotatingSender_DefaultMode_AllowsAllVendors(t *testing.T) {
	// Default behaviour (LRU across all) is preserved.
	db := openTestDB(t)
	ctx := context.Background()

	var rCalls, bCalls atomic.Int32
	rs, err := NewRotatingSender(db,
		[]Client{
			newStubClient("resend", &rCalls),
			newStubClient("brevo", &bCalls),
		},
		[]string{"r1", "b1"})
	if err != nil {
		t.Fatalf("NewRotatingSender: %v", err)
	}
	if err := rs.Send(ctx, Email{
		To:             []string{"jaslian@gmail.com"},
		IdempotencyKey: "default-mode-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if rCalls.Load()+bCalls.Load() != 1 {
		t.Errorf("expected exactly one call across all clients, got r=%d b=%d",
			rCalls.Load(), bCalls.Load())
	}
}

func TestRotatingSender_PicksOldestKey(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Pre-record: key2 is most recently used, key1 is older.
	now := time.Now().Unix()
	if err := db.RecordKeyUse(ctx, notifydb.KeyUse{Vendor: "resend", KeyID: "k1", LastUsedUnix: now - 200}); err != nil {
		t.Fatalf("record k1: %v", err)
	}
	if err := db.RecordKeyUse(ctx, notifydb.KeyUse{Vendor: "resend", KeyID: "k2", LastUsedUnix: now - 50}); err != nil {
		t.Fatalf("record k2: %v", err)
	}

	var c1Calls, c2Calls atomic.Int32
	rs, err := NewRotatingSender(db,
		[]Client{newStubClient("resend", &c1Calls), newStubClient("resend", &c2Calls)},
		[]string{"k1", "k2"})
	if err != nil {
		t.Fatalf("NewRotatingSender: %v", err)
	}

	err = rs.Send(ctx, Email{
		To:             []string{"jaslian@gmail.com"},
		IdempotencyKey: "lru-pick-1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if c1Calls.Load() != 1 {
		t.Errorf("expected c1 (oldest, k1) to receive the call, got c1=%d c2=%d", c1Calls.Load(), c2Calls.Load())
	}
	if c2Calls.Load() != 0 {
		t.Errorf("expected c2 (newer, k2) to be skipped, got c2=%d", c2Calls.Load())
	}

	// Verify the use was recorded.
	uses, _ := db.ListKeyUses(ctx, "resend")
	if len(uses) != 2 {
		t.Fatalf("expected 2 key rows, got %d", len(uses))
	}
	// After the pick, k1 should be the most recently used.
	if uses[0].KeyID != "k1" {
		t.Errorf("expected k1 to be most recently used after Send, got %s", uses[0].KeyID)
	}
}

func TestRotatingSender_RotatesAcrossSends(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var c1Calls, c2Calls atomic.Int32
	rs, err := NewRotatingSender(db,
		[]Client{newStubClient("resend", &c1Calls), newStubClient("resend", &c2Calls)},
		[]string{"k1", "k2"})
	if err != nil {
		t.Fatalf("NewRotatingSender: %v", err)
	}

	// First send: both keys unseen → defaults to idx 0 (k1).
	if err := rs.Send(ctx, Email{To: []string{"jaslian@gmail.com"}, IdempotencyKey: "r1"}); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	if c1Calls.Load() != 1 {
		t.Errorf("expected k1 first call, got c1=%d c2=%d", c1Calls.Load(), c2Calls.Load())
	}

	// Second send: k1 was just used, k2 is unseen → picks k2.
	if err := rs.Send(ctx, Email{To: []string{"jaslian@gmail.com"}, IdempotencyKey: "r2"}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	if c2Calls.Load() != 1 {
		t.Errorf("expected k2 second call, got c1=%d c2=%d", c1Calls.Load(), c2Calls.Load())
	}

	// Third send: k2 just used, k1 older → picks k1 again.
	if err := rs.Send(ctx, Email{To: []string{"jaslian@gmail.com"}, IdempotencyKey: "r3"}); err != nil {
		t.Fatalf("send 3: %v", err)
	}
	if c1Calls.Load() != 2 {
		t.Errorf("expected k1 third call, got c1=%d c2=%d", c1Calls.Load(), c2Calls.Load())
	}
}

func TestRotatingSender_RejectsNonCanonical(t *testing.T) {
	db := openTestDB(t)
	var c1Calls atomic.Int32
	rs, _ := NewRotatingSender(db,
		[]Client{newStubClient("resend", &c1Calls)},
		[]string{"k1"})
	err := rs.Send(context.Background(), Email{
		To:             []string{"attacker@example.com"},
		IdempotencyKey: "reject-1",
	})
	if err == nil {
		t.Fatal("expected rejection")
	}
	if c1Calls.Load() != 0 {
		t.Errorf("expected no client call, got %d", c1Calls.Load())
	}
}

func TestRotatingSender_RequiresIdempotencyKey(t *testing.T) {
	db := openTestDB(t)
	rs, _ := NewRotatingSender(db,
		[]Client{newStubClient("resend", &atomic.Int32{})},
		[]string{"k1"})
	err := rs.Send(context.Background(), Email{
		To: []string{"jaslian@gmail.com"},
	})
	if err == nil {
		t.Fatal("expected error on missing idempotency key")
	}
}

func TestRotatingSender_RejectsEmptyKeyID(t *testing.T) {
	db := openTestDB(t)
	_, err := NewRotatingSender(db,
		[]Client{newStubClient("resend", &atomic.Int32{})},
		[]string{""})
	if err == nil {
		t.Fatal("expected error on empty key_id")
	}
}

func TestRotatingSender_RejectsLengthMismatch(t *testing.T) {
	db := openTestDB(t)
	_, err := NewRotatingSender(db,
		[]Client{newStubClient("resend", &atomic.Int32{}), newStubClient("resend", &atomic.Int32{})},
		[]string{"k1"})
	if err == nil {
		t.Fatal("expected length mismatch error")
	}
}

func TestRotatingSender_FallsBackWhenDBNil(t *testing.T) {
	var c1Calls, c2Calls atomic.Int32
	rs, _ := NewRotatingSender(nil, // no DB
		[]Client{newStubClient("resend", &c1Calls), newStubClient("resend", &c2Calls)},
		[]string{"k1", "k2"})
	// Without a DB we have no LRU signal → falls back to idx 0.
	if err := rs.Send(context.Background(), Email{
		To: []string{"jaslian@gmail.com"}, IdempotencyKey: "nil-db-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if c1Calls.Load() != 1 {
		t.Errorf("expected c1 (idx 0) fallback, got c1=%d c2=%d", c1Calls.Load(), c2Calls.Load())
	}
}

// TestRotatingSender_EndToEnd_WithHTTPServer verifies that the LRU
// logic does not break the HTTP path: a real httptest.Server backs
// the stub client and the LRU pick still routes correctly.
func TestRotatingSender_EndToEnd_WithHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer func() { srv.Close() }()

	db := openTestDB(t)
	c1 := NewResendClient(ResendConfig{APIKey: "k", BaseURL: srv.URL})
	c2 := NewBrevoClient(BrevoConfig{APIKey: "k", BaseURL: srv.URL})
	rs, err := NewRotatingSender(db, []Client{c1, c2}, []string{"k1", "k2"})
	if err != nil {
		t.Fatalf("NewRotatingSender: %v", err)
	}
	if err := rs.Send(context.Background(), Email{
		To: []string{"jaslian@gmail.com"}, IdempotencyKey: "e2e-1", Subject: "hi",
	}); err != nil {
		t.Errorf("Send: %v", err)
	}
}
