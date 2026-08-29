// Tests for the connection settings in Open's DSN. v18784.
package notifydb

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestDSNPragmasApplyToEveryPooledConnection is the positive control for the
// store configuration.
//
// synchronous and busy_timeout are per-CONNECTION settings and database/sql
// hands out a pool, so a `PRAGMA ...` issued through conn.ExecContext after
// Open configures exactly one connection and silently leaves every other one
// at its default. Carrying them in the DSN is what makes them reach the pool.
//
// The test reserves several connections SIMULTANEOUSLY, so the pool is forced
// to open more than one rather than handing the same connection back, and
// checks each. Move dsnPragmas into a post-Open PRAGMA statement and this
// fails on every connection but one.
func TestDSNPragmasApplyToEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	const conns = 6
	held := make([]*sql.Conn, 0, conns)
	t.Cleanup(func() {
		for _, c := range held {
			_ = c.Close()
		}
	})
	// Reserved together and released only at the end, so these are guaranteed
	// to be distinct connections.
	//
	// The bounded context is deliberate: Open does not cap the pool today, but
	// a later SetMaxOpenConns(n < conns) would make Conn() block forever on a
	// background context, and this package's failure mode of choice is a clear
	// message rather than a test binary that hangs to its timeout and dumps a
	// goroutine stack parked in the pool.
	acquire, cancelAcquire := context.WithTimeout(ctx, 30*time.Second)
	defer cancelAcquire()
	for i := 0; i < conns; i++ {
		c, err := db.conn.Conn(acquire)
		if err != nil {
			t.Fatalf("reserve conn %d of %d: %v (a capped pool cannot satisfy this test)", i, conns, err)
		}
		held = append(held, c)
	}

	for i, c := range held {
		var sync int
		if err := c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatalf("conn %d: read synchronous: %v", i, err)
		}
		// 1 == NORMAL. At the default 2 (FULL) every Insert is an fsync
		// barrier — measured on this package at 863ms mean and 2.93s worst on
		// a loaded host, spent on a deadline the audit write shares with the
		// send it is recording.
		if sync != 1 {
			t.Errorf("conn %d: synchronous = %d, want 1 (NORMAL)", i, sync)
		}

		var jm string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&jm); err != nil {
			t.Fatalf("conn %d: read journal_mode: %v", i, err)
		}
		// NORMAL is only safe against corruption in WAL. If journal_mode ever
		// stops being WAL, synchronous must be revisited with it.
		if jm != "wal" {
			t.Errorf("conn %d: journal_mode = %q, want \"wal\"", i, jm)
		}

		var busy int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("conn %d: read busy_timeout: %v", i, err)
		}
		if busy != 5000 {
			t.Errorf("conn %d: busy_timeout = %d, want 5000", i, busy)
		}
	}
}

// TestInsertSurvivesAConnectionItDidNotOpen is the behavioral half: a row
// written while the pool is saturated must still be readable. It fails if a
// pooled connection is left at a setting that makes the write error out.
func TestInsertSurvivesAConnectionItDidNotOpen(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// Hold connections so Insert has to open a fresh one of its own.
	held := make([]*sql.Conn, 0, 4)
	t.Cleanup(func() {
		for _, c := range held {
			_ = c.Close()
		}
	})
	acquire, cancelAcquire := context.WithTimeout(ctx, 30*time.Second)
	defer cancelAcquire()
	for i := 0; i < 4; i++ {
		c, err := db.conn.Conn(acquire)
		if err != nil {
			t.Fatalf("reserve conn %d of 4: %v (a capped pool cannot satisfy this test)", i, err)
		}
		held = append(held, c)
	}

	row := Dispatch{
		ID: "pooled-1", Vendor: "resend", Recipient: "x@y.z",
		Status: "ok", CreatedUnix: 1700000000, Attempt: 1,
	}
	if err := db.Insert(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, found, err := db.Get(ctx, "pooled-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("Get: row written on a saturated pool was not found")
	}
	if got.Vendor != "resend" {
		t.Errorf("Get: vendor = %q, want \"resend\"", got.Vendor)
	}
}
