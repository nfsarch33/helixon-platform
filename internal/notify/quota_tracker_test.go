package notify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

// fakeBrevoClient is a minimal Client implementation for tracker tests.
// It records Send calls and returns no error.
type fakeBrevoClient struct {
	calls int
}

func (f *fakeBrevoClient) Send(ctx context.Context, m Email) error { //nolint:revive // unused-parameter required by interface
	f.calls++
	return nil
}
func (f *fakeBrevoClient) Vendor() string { return "brevo" }

// fakeResendClient mirrors the Resend vendor name to verify the
// tracker rejects non-Brevo clients at construction.
type fakeResendClient struct{}

func (f *fakeResendClient) Send(ctx context.Context, m Email) error { return nil } //nolint:revive // unused-parameter required by interface
func (f *fakeResendClient) Vendor() string                          { return "resend" }

// newTestDB returns a fresh temp-file notifydb for tracker tests.
// Temp file because notifydb.Open requires a path (no in-memory variant).
func newTestDB(t *testing.T) *notifydb.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "notifydb.sqlite")
	db, err := notifydb.Open(path, nil)
	if err != nil {
		t.Fatalf("open temp notifydb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestBrevoQuotaTracker_New_RejectsNonBrevo(t *testing.T) {
	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}, &fakeResendClient{}}
	_, err := NewBrevoQuotaTracker(db, clients, []string{"b1", "r1"}, 24*time.Hour)
	if err == nil {
		t.Fatal("expected error for non-Brevo client, got nil")
	}
	if !strings.Contains(err.Error(), "brevo") {
		t.Errorf("error message should mention brevo, got: %v", err)
	}
}

func TestBrevoQuotaTracker_New_RejectsEmptyClients(t *testing.T) {
	db := newTestDB(t)
	_, err := NewBrevoQuotaTracker(db, nil, nil, 24*time.Hour)
	if err == nil {
		t.Fatal("expected error for empty clients, got nil")
	}
}

func TestBrevoQuotaTracker_New_RejectsBadWarnAfter(t *testing.T) {
	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}}
	for _, bad := range []time.Duration{0, -1 * time.Hour} {
		_, err := NewBrevoQuotaTracker(db, clients, []string{"b1"}, bad)
		if err == nil {
			t.Fatalf("expected error for warnAfter=%v, got nil", bad)
		}
	}
}

func TestBrevoQuotaTracker_Observe_AlertWhenNoUses(t *testing.T) {
	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}}
	tr, err := NewBrevoQuotaTracker(db, clients, []string{"b1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	status, err := tr.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if status.Level != "alert" {
		t.Errorf("expected level=alert when no uses recorded, got %q", status.Level)
	}
}

func TestBrevoQuotaTracker_Observe_OKWhenFresh(t *testing.T) {
	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}}
	tr, err := NewBrevoQuotaTracker(db, clients, []string{"b1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Record a use as of 1 hour ago — fresh, no warning.
	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	if err := db.RecordKeyUse(context.Background(), notifydb.KeyUse{
		Vendor: "brevo", KeyID: "b1", LastUsedUnix: oneHourAgo,
	}); err != nil {
		t.Fatalf("record use: %v", err)
	}
	status, err := tr.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if status.Level != "ok" {
		t.Errorf("expected level=ok for 1h-old use with 24h warn, got %q delta=%v", status.Level, status.Delta)
	}
}

func TestBrevoQuotaTracker_Observe_WarnWhenOld(t *testing.T) {
	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}}
	tr, err := NewBrevoQuotaTracker(db, clients, []string{"b1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Record a use as of 30 hours ago — over the 24h warn threshold.
	thirtyHoursAgo := time.Now().Add(-30 * time.Hour).Unix()
	if err := db.RecordKeyUse(context.Background(), notifydb.KeyUse{
		Vendor: "brevo", KeyID: "b1", LastUsedUnix: thirtyHoursAgo,
	}); err != nil {
		t.Fatalf("record use: %v", err)
	}
	status, err := tr.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if status.Level != "warn" {
		t.Errorf("expected level=warn for 30h-old use with 24h warn, got %q delta=%v", status.Level, status.Delta)
	}
}

func TestBrevoQuotaTracker_Observe_AlertWhenApproachingDeletion(t *testing.T) {
	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}}
	tr, err := NewBrevoQuotaTracker(db, clients, []string{"b1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Record a use as of 80 days ago — past the 76-day alert threshold
	// (90 - 14 days grace).
	eightyDaysAgo := time.Now().Add(-80 * 24 * time.Hour).Unix()
	if err := db.RecordKeyUse(context.Background(), notifydb.KeyUse{
		Vendor: "brevo", KeyID: "b1", LastUsedUnix: eightyDaysAgo,
	}); err != nil {
		t.Fatalf("record use: %v", err)
	}
	status, err := tr.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if status.Level != "alert" {
		t.Errorf("expected level=alert for 80d-old use, got %q delta=%v", status.Level, status.Delta)
	}
}

func TestBrevoQuotaTracker_Observe_PicksOldestAcrossKeys(t *testing.T) {
	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}, &fakeBrevoClient{}}
	tr, err := NewBrevoQuotaTracker(db, clients, []string{"b1", "b2"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// b1 is fresh; b2 is ancient. Oldest delta = b2's, so level = alert.
	now := time.Now()
	if err := db.RecordKeyUse(context.Background(), notifydb.KeyUse{
		Vendor: "brevo", KeyID: "b1", LastUsedUnix: now.Add(-1 * time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("record b1: %v", err)
	}
	if err := db.RecordKeyUse(context.Background(), notifydb.KeyUse{
		Vendor: "brevo", KeyID: "b2", LastUsedUnix: now.Add(-100 * 24 * time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("record b2: %v", err)
	}
	status, err := tr.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if status.Level != "alert" {
		t.Errorf("expected level=alert (driven by b2), got %q delta=%v", status.Level, status.Delta)
	}
}

func TestBrevoQuotaTracker_Emit_NDJSONLine(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	db := newTestDB(t)
	clients := []Client{&fakeBrevoClient{}}
	tr, err := NewBrevoQuotaTracker(db, clients, []string{"b1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Force an alert by recording no uses.
	tr.emit(QuotaStatus{Level: "alert", Delta: 48 * time.Hour, OldestUse: time.Now().Add(-48 * time.Hour)})

	logPath := filepath.Join(tmp, "logs", "runx", "agentrace-mcp.ndjson")
	data, err := os.ReadFile(logPath) //nolint:gosec // G304 test fixture
	if err != nil {
		t.Fatalf("read agentrace log: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("agentrace log is empty after emit")
	}
	var line map[string]any
	if err := json.Unmarshal(data[:strings.IndexByte(string(data), '\n')], &line); err != nil {
		t.Fatalf("parse first NDJSON line: %v", err)
	}
	if line["event"] != "brevo_quota_warning" {
		t.Errorf("expected event=brevo_quota_warning, got %v", line["event"])
	}
	if line["vendor"] != "brevo" {
		t.Errorf("expected vendor=brevo, got %v", line["vendor"])
	}
	if line["level"] != "alert" {
		t.Errorf("expected level=alert, got %v", line["level"])
	}
}
