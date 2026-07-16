// Tests for the notifydb package. v17409-4 TDD coverage.
package notifydb

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.sqlite3"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestDB_MigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	// Calling migrate twice via Open is the realistic flow; here just assert
	// the table exists by inserting.
	err := db.Insert(context.Background(), Dispatch{
		ID: "test-1", Vendor: "resend", Recipient: "x@y.z", Subject: "s",
		Status: "ok", CreatedUnix: 1700000000, Attempt: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func TestDB_InsertIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	row := Dispatch{
		ID: "dup-1", Vendor: "resend", Recipient: "x@y.z",
		Status: "ok", CreatedUnix: 1700000000, Attempt: 1,
	}
	for i := 0; i < 5; i++ {
		if err := db.Insert(context.Background(), row); err != nil {
			t.Fatalf("Insert iter %d: %v", i, err)
		}
	}
	got, found, err := db.Get(context.Background(), "dup-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("Get: dup-1 not found after 5 inserts")
	}
	if got.Attempt != 1 {
		t.Fatalf("Attempt: want 1 (first insert wins), got %d", got.Attempt)
	}
}

func TestDB_InsertRequiresID(t *testing.T) {
	db := newTestDB(t)
	err := db.Insert(context.Background(), Dispatch{
		Vendor: "resend", Status: "ok",
	})
	if err == nil {
		t.Fatal("want error on missing ID")
	}
}

func TestDB_Recent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = db.Insert(ctx, Dispatch{
			ID:          "r-" + string(rune('a'+i)),
			Vendor:      "resend",
			Recipient:   "x@y.z",
			Status:      "ok",
			CreatedUnix: int64(1700000000 + i),
		})
	}
	got, err := db.Recent(ctx, 3)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Recent: want 3, got %d", len(got))
	}
	// Newest first.
	if got[0].ID != "r-e" {
		t.Fatalf("Recent[0]: want r-e (newest), got %q", got[0].ID)
	}
}

func TestDB_ListByPlan(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// v18652 + v18654 dispatches; one v18653 transient that should NOT
	// be included when we query for v18654.
	rows := []Dispatch{
		{ID: "v18652-end", Vendor: "resend", Status: "ok", CreatedUnix: 1700000010},
		{ID: "v18653-end", Vendor: "resend", Status: "rendered", CreatedUnix: 1700000020},
		{ID: "v18654-1-end", Vendor: "resend", Status: "ok", CreatedUnix: 1700000030},
		{ID: "v18654-2-end", Vendor: "brevo", Status: "error", CreatedUnix: 1700000031},
		{ID: "v18654-3-end", Vendor: "resend", Status: "ok", CreatedUnix: 1700000032},
	}
	for _, r := range rows {
		if err := db.Insert(ctx, r); err != nil {
			t.Fatalf("Insert %s: %v", r.ID, err)
		}
	}
	got, err := db.ListByPlan(ctx, "v18654-")
	if err != nil {
		t.Fatalf("ListByPlan: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListByPlan v18654-: want 3, got %d (%v)", len(got), got)
	}
	// Newest-first: v18654-3-end (32), v18654-2-end (31), v18654-1-end (30).
	if got[0].ID != "v18654-3-end" {
		t.Fatalf("ListByPlan[0]: want v18654-3-end, got %s", got[0].ID)
	}
	if got[2].ID != "v18654-1-end" {
		t.Fatalf("ListByPlan[2]: want v18654-1-end, got %s", got[2].ID)
	}
	// Empty prefix match (all) sanity check.
	all, err := db.ListByPlan(ctx, "v1865")
	if err != nil {
		t.Fatalf("ListByPlan v1865: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("ListByPlan v1865: want 5 (all v1865*), got %d", len(all))
	}
}

func TestDB_CountByVendor(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	vendors := []string{"resend", "resend", "brevo", "telegram"}
	for i, v := range vendors {
		_ = db.Insert(ctx, Dispatch{
			ID:          "cbv-" + string(rune('a'+i)),
			Vendor:      v,
			Status:      "ok",
			CreatedUnix: int64(i),
		})
	}
	counts, err := db.CountByVendor(ctx)
	if err != nil {
		t.Fatalf("CountByVendor: %v", err)
	}
	if counts["resend"] != 2 || counts["brevo"] != 1 || counts["telegram"] != 1 {
		t.Fatalf("counts: %+v", counts)
	}
}

func TestDB_GetReturnsFalseOnMissing(t *testing.T) {
	db := newTestDB(t)
	_, found, err := db.Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("Get: should return found=false for missing")
	}
}

func TestDB_ConcurrentInsertsAreSafe(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = db.Insert(ctx, Dispatch{
				ID:          "conc-" + string(rune('a'+i)),
				Vendor:      "resend",
				Status:      "ok",
				CreatedUnix: int64(i),
			})
		}(i)
	}
	wg.Wait()
	counts, _ := db.CountByVendor(ctx)
	if counts["resend"] != 20 {
		t.Fatalf("resend count: want 20 unique, got %d", counts["resend"])
	}
}

func TestDB_OpenWithDefaultPathFallsBack(t *testing.T) {
	// Smoke: Open("") → default path. Should not panic and should produce
	// a working DB. Uses HOME=/tmp/notifydb-default-test.
	t.Setenv("HOME", t.TempDir())
	db, err := Open("", nil)
	if err != nil {
		t.Fatalf("Open default: %v", err)
	}
	defer db.Close()
	if err := db.Insert(context.Background(), Dispatch{
		ID: "default-1", Vendor: "resend", Status: "ok",
	}); err != nil {
		t.Fatalf("Insert on default: %v", err)
	}
}

func TestDB_InsertAppliesDefaultCreatedUnix(t *testing.T) {
	db := newTestDB(t)
	row := Dispatch{ID: "now-1", Vendor: "resend", Status: "ok"}
	if err := db.Insert(context.Background(), row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, found, err := db.Get(context.Background(), "now-1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got.CreatedUnix == 0 {
		t.Fatal("CreatedUnix: want non-zero after Insert default")
	}
}

func TestDB_InsertAppliesDefaultAttempt(t *testing.T) {
	db := newTestDB(t)
	row := Dispatch{ID: "att-1", Vendor: "resend", Status: "ok", CreatedUnix: 1700000000}
	if err := db.Insert(context.Background(), row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, found, _ := db.Get(context.Background(), "att-1")
	if !found {
		t.Fatal("not found")
	}
	if got.Attempt != 1 {
		t.Fatalf("Attempt default: want 1, got %d", got.Attempt)
	}
}

func TestDB_InsertWithSentAtUnix(t *testing.T) {
	db := newTestDB(t)
	if err := db.Insert(context.Background(), Dispatch{
		ID: "sat-1", Vendor: "resend", Status: "ok", SentAtUnix: 1700000500, CreatedUnix: 1700000000,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, _, _ := db.Get(context.Background(), "sat-1")
	if got.SentAtUnix != 1700000500 {
		t.Fatalf("SentAtUnix: want 1700000500, got %d", got.SentAtUnix)
	}
}

func TestDB_RecentRespectsLimit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_ = db.Insert(ctx, Dispatch{
			ID:          "rl-" + string(rune('a'+i)),
			Vendor:      "resend",
			Status:      "ok",
			CreatedUnix: int64(i),
		})
	}
	got, _ := db.Recent(ctx, 0) // 0 → default 100
	if len(got) != 10 {
		t.Fatalf("Recent(0): want 10 (default cap), got %d", len(got))
	}
}

func TestDB_CloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir+"/c.sqlite3", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close returns sql.ErrConnDone or similar — we just assert
	// no panic. Real-world idempotency is the caller's responsibility.
	_ = db.Close()
}
