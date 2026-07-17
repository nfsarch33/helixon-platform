// Package notifydb — v17607-6 LRU sender tracking tests.
//
// The v17607-6c RotatingSender picks the least-recently-used vendor key
// based on last_used_at tracked in a new notifydb column. This file
// defines the contract; the GREEN implementation lives in lru.go.
package notifydb

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestKeyUsage_RecordAndList covers the happy path: record two keys
// for the same vendor at distinct timestamps, then list them and
// verify the list reflects the most-recent-first ordering.
func TestKeyUsage_RecordAndList(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.sqlite3"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	now := time.Now().Unix()
	if err := db.RecordKeyUse(ctx, KeyUse{Vendor: "resend", KeyID: "key1", LastUsedUnix: now - 100}); err != nil {
		t.Fatalf("RecordKeyUse key1: %v", err)
	}
	if err := db.RecordKeyUse(ctx, KeyUse{Vendor: "resend", KeyID: "key2", LastUsedUnix: now - 50}); err != nil {
		t.Fatalf("RecordKeyUse key2: %v", err)
	}

	got, err := db.ListKeyUses(ctx, "resend")
	if err != nil {
		t.Fatalf("ListKeyUses: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(got))
	}
	// Most-recently-used should come first.
	if got[0].KeyID != "key2" {
		t.Errorf("expected key2 first (most recent), got %s", got[0].KeyID)
	}
	if got[1].KeyID != "key1" {
		t.Errorf("expected key1 second, got %s", got[1].KeyID)
	}
}

// TestKeyUsage_Upsert verifies that re-recording a key updates the
// timestamp instead of inserting a duplicate row.
func TestKeyUsage_Upsert(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.sqlite3"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	now := time.Now().Unix()
	if err := db.RecordKeyUse(ctx, KeyUse{Vendor: "brevo", KeyID: "key1", LastUsedUnix: now - 100}); err != nil {
		t.Fatalf("RecordKeyUse 1: %v", err)
	}
	if err := db.RecordKeyUse(ctx, KeyUse{Vendor: "brevo", KeyID: "key1", LastUsedUnix: now}); err != nil {
		t.Fatalf("RecordKeyUse 2 (upsert): %v", err)
	}

	got, err := db.ListKeyUses(ctx, "brevo")
	if err != nil {
		t.Fatalf("ListKeyUses: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(got))
	}
	if got[0].LastUsedUnix != now {
		t.Errorf("expected LastUsedUnix=%d, got %d", now, got[0].LastUsedUnix)
	}
}

// TestKeyUsage_PickLeastRecentlyUsed verifies the LRU pick helper.
func TestKeyUsage_PickLeastRecentlyUsed(t *testing.T) {
	keys := []KeyUse{
		{Vendor: "resend", KeyID: "k1", LastUsedUnix: 200},
		{Vendor: "resend", KeyID: "k2", LastUsedUnix: 100},
		{Vendor: "resend", KeyID: "k3", LastUsedUnix: 300},
	}
	got, err := PickLeastRecentlyUsed(keys)
	if err != nil {
		t.Fatalf("PickLeastRecentlyUsed: %v", err)
	}
	if got.KeyID != "k2" {
		t.Errorf("expected k2 (oldest), got %s", got.KeyID)
	}
}

// TestKeyUsage_PickLeastRecentlyUsed_Empty verifies the empty-list case.
func TestKeyUsage_PickLeastRecentlyUsed_Empty(t *testing.T) {
	_, err := PickLeastRecentlyUsed(nil)
	if err == nil {
		t.Fatal("expected error on empty list")
	}
}

// TestKeyUsage_ListKeyUses_EmptyVendor verifies filtering by vendor
// returns an empty slice when no keys have been recorded.
func TestKeyUsage_ListKeyUses_EmptyVendor(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.sqlite3"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	got, err := db.ListKeyUses(context.Background(), "neverused")
	if err != nil {
		t.Fatalf("ListKeyUses: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list for unused vendor, got %d", len(got))
	}
}
