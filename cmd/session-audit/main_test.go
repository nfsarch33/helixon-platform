// runx-public-repo-gate: allow-file personal_path_id
// Tests for cmd/session-audit. v18654-2 TDD coverage.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

func setupTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sa.sqlite3")
	db, err := notifydb.Open(path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	for _, r := range []notifydb.Dispatch{
		{ID: "v18652-end", Vendor: "resend", Recipient: "jaslian@gmail.com", Subject: "[END] v18652", Status: "ok", CreatedUnix: 1700000010},
		{ID: "v18653-1-end", Vendor: "resend", Status: "ok", CreatedUnix: 1700000020},
		{ID: "v18653-2-end", Vendor: "brevo", Status: "rendered", CreatedUnix: 1700000021},
		{ID: "v18654-1-end", Vendor: "telegram", Status: "ok", CreatedUnix: 1700000030},
	} {
		if err := db.Insert(ctx, r); err != nil {
			t.Fatalf("Insert %s: %v", r.ID, err)
		}
	}
	return path
}

func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	rc := fn()
	_ = w.Close()
	os.Stdout = old
	var buf strings.Builder
	bs := make([]byte, 4096)
	for {
		n, err := r.Read(bs)
		if n > 0 {
			buf.Write(bs[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String(), rc
}

func TestRunSessionAudit_PlanFilter_NDJSON(t *testing.T) {
	dbPath := setupTestDB(t)
	out, rc := captureStdout(t, func() int {
		return runSessionAudit("v18653-", dbPath, false)
	})
	if rc != 0 {
		t.Fatalf("runSessionAudit: want rc=0, got %d", rc)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("plan filter v18653-: want 2 lines, got %d (%q)", len(lines), out)
	}
	// Newest first.
	var r notifydb.Dispatch
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil {
		t.Fatalf("unmarshal[0]: %v", err)
	}
	if r.ID != "v18653-2-end" {
		t.Fatalf("lines[0]: want v18653-2-end (newer), got %s", r.ID)
	}
}

func TestRunSessionAudit_PlanFilter_JSON(t *testing.T) {
	dbPath := setupTestDB(t)
	out, rc := captureStdout(t, func() int {
		return runSessionAudit("v18652-", dbPath, true)
	})
	if rc != 0 {
		t.Fatalf("rc=0 expected, got %d", rc)
	}
	var arr []notifydb.Dispatch
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 1 || arr[0].ID != "v18652-end" {
		t.Fatalf("want 1 row v18652-end, got %d rows: %+v", len(arr), arr)
	}
}

func TestRunSessionAudit_EmptyPlanReturnsAll(t *testing.T) {
	dbPath := setupTestDB(t)
	out, rc := captureStdout(t, func() int {
		return runSessionAudit("", dbPath, true)
	})
	if rc != 0 {
		t.Fatalf("rc=0 expected, got %d", rc)
	}
	var arr []notifydb.Dispatch
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 4 {
		t.Fatalf("empty plan should match %% => all 4 rows, got %d", len(arr))
	}
}

func TestRunSessionAudit_BadDBReturnsRc2(t *testing.T) {
	rc := 0
	func() {
		r, w, _ := os.Pipe()
		old := os.Stdout
		oldErr := os.Stderr
		os.Stdout = w
		os.Stderr = w
		rc = runSessionAudit("v18654-", "/nonexistent/path/db.sqlite3", false)
		_ = w.Close()
		os.Stdout = old
		os.Stderr = oldErr
		_ = r
	}()
	if rc != 2 {
		t.Fatalf("bad db path: want rc=2, got %d", rc)
	}
}
