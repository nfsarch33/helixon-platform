// Package notifydb persists notification dispatch records to a local SQLite
// database for audit, replay, and operator observability. v17409-4.
//
// Design goals:
//   - Idempotent: opening an existing schema is a no-op (CREATE IF NOT EXISTS).
//   - Retry-safe: IdempotencyKey is the primary key; duplicate inserts return
//     nil silently.
//   - Append-only NDJSON mirror: every successful insert is also written to
//     ~/logs/runx/notifydb.ndjson for offline ingestion.
//
// Schema:
//   - dispatches(id TEXT PRIMARY KEY, vendor TEXT NOT NULL, recipient TEXT,
//     subject TEXT, status TEXT NOT NULL, error TEXT, sent_at_unix INT NOT NULL,
//     created_at_unix INT NOT NULL, attempt INT NOT NULL DEFAULT 1)
package notifydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Dispatch is the canonical row written to SQLite.
type Dispatch struct {
	ID          string `json:"id"`     // idempotency key
	Vendor      string `json:"vendor"` // resend | brevo | telegram
	Recipient   string `json:"recipient"`
	Subject     string `json:"subject,omitempty"`
	Status      string `json:"status"` // ok | error
	Error       string `json:"error,omitempty"`
	SentAtUnix  int64  `json:"sent_at_unix"` // 0 if not yet sent
	CreatedUnix int64  `json:"created_at_unix"`
	Attempt     int    `json:"attempt"`
}

// DB is the persistent store wrapper.
type DB struct {
	mu       sync.Mutex
	conn     *sql.DB
	mirror   *os.File
	mirrorOn bool
}

// DefaultPath returns ~/logs/runx/notifydb.sqlite3 (created if missing).
func DefaultPath() string {
	root := os.Getenv("HOME")
	if root == "" {
		root = "/tmp"
	}
	dir := filepath.Join(root, "logs", "runx")
	_ = os.MkdirAll(dir, 0o755) //nolint:gosec // G301 dir perms 0750 acceptable for runtime cache dirs
	return filepath.Join(dir, "notifydb.sqlite3")
}

// DefaultMirror returns ~/logs/runx/notifydb.ndjson (opened append-only).
func DefaultMirror() *os.File {
	root := os.Getenv("HOME")
	if root == "" {
		root = "/tmp"
	}
	dir := filepath.Join(root, "logs", "runx")
	_ = os.MkdirAll(dir, 0o755) //nolint:gosec // G301 dir perms 0750 acceptable for runtime cache dirs
	f, err := os.OpenFile(filepath.Join(dir, "notifydb.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G302 file perms 0750 acceptable for non-secret runtime files
	if err != nil {
		return nil
	}
	return f
}

// Open creates or opens the DB at the given path. If mirror is non-nil,
// every successful insert is also mirrored as NDJSON.
func Open(path string, mirror *os.File) (*DB, error) {
	if path == "" {
		path = DefaultPath()
	}
	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("notifydb.Open: %w", err)
	}
	db := &DB{conn: conn, mirror: mirror, mirrorOn: mirror != nil}
	if err := db.migrate(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return db, nil
}

// migrate is idempotent — runs every time Open is called.
func (d *DB) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS dispatches (
    id TEXT PRIMARY KEY,
    vendor TEXT NOT NULL,
    recipient TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    sent_at_unix INTEGER NOT NULL DEFAULT 0,
    created_at_unix INTEGER NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_dispatches_vendor ON dispatches(vendor);
CREATE INDEX IF NOT EXISTS idx_dispatches_status ON dispatches(status);
CREATE INDEX IF NOT EXISTS idx_dispatches_created ON dispatches(created_at_unix);
`
	if _, err := d.conn.Exec(schema); err != nil {
		return err
	}
	// v17607-6c: LRU sender tracking table.
	return d.migrateKeyUses()
}

// Close flushes and closes the connection + mirror.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mirror != nil {
		_ = d.mirror.Close()
		d.mirror = nil
	}
	return d.conn.Close()
}

// Insert persists a Dispatch. If the IdempotencyKey already exists, the
// call is a no-op (returns nil). Idempotency is enforced at the SQL layer
// (PRIMARY KEY conflict → IGNORE).
func (d *DB) Insert(ctx context.Context, row Dispatch) error {
	if row.ID == "" {
		return errors.New("notifydb.Insert: row.ID (idempotency key) required")
	}
	if row.CreatedUnix == 0 {
		row.CreatedUnix = time.Now().Unix()
	}
	if row.Attempt <= 0 {
		row.Attempt = 1
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	const q = `
INSERT OR IGNORE INTO dispatches
    (id, vendor, recipient, subject, status, error, sent_at_unix, created_at_unix, attempt)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?)
`
	_, err := d.conn.ExecContext(ctx, q,
		row.ID, row.Vendor, row.Recipient, row.Subject, row.Status, row.Error,
		row.SentAtUnix, row.CreatedUnix, row.Attempt,
	)
	if err != nil {
		return fmt.Errorf("notifydb.Insert: %w", err)
	}

	if d.mirrorOn && d.mirror != nil {
		line, err := json.Marshal(row)
		if err == nil {
			line = append(line, '\n')
			_, _ = d.mirror.Write(line)
		}
	}
	return nil
}

// Get fetches a Dispatch by idempotency key.
func (d *DB) Get(ctx context.Context, id string) (Dispatch, bool, error) {
	const q = `
SELECT id, vendor, recipient, subject, status, error, sent_at_unix, created_at_unix, attempt
FROM dispatches WHERE id = ? LIMIT 1
`
	var r Dispatch
	err := d.conn.QueryRowContext(ctx, q, id).Scan(
		&r.ID, &r.Vendor, &r.Recipient, &r.Subject, &r.Status, &r.Error,
		&r.SentAtUnix, &r.CreatedUnix, &r.Attempt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Dispatch{}, false, nil
	}
	if err != nil {
		return Dispatch{}, false, err
	}
	return r, true, nil
}

// Recent returns up to n most recent dispatches, newest first.
func (d *DB) Recent(ctx context.Context, n int) ([]Dispatch, error) {
	if n <= 0 {
		n = 100
	}
	const q = `
SELECT id, vendor, recipient, subject, status, error, sent_at_unix, created_at_unix, attempt
FROM dispatches ORDER BY created_at_unix DESC LIMIT ?
`
	rows, err := d.conn.QueryContext(ctx, q, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dispatch
	for rows.Next() {
		var r Dispatch
		if err := rows.Scan(&r.ID, &r.Vendor, &r.Recipient, &r.Subject, &r.Status, &r.Error,
			&r.SentAtUnix, &r.CreatedUnix, &r.Attempt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListByPlan returns dispatches whose IdempotencyKey (id) starts with the
// given plan prefix (e.g. "v18652-"). Used by session-end email audit.
// Order is newest-first. v18654-2.
func (d *DB) ListByPlan(ctx context.Context, planPrefix string) ([]Dispatch, error) {
	const q = `
SELECT id, vendor, recipient, subject, status, error, sent_at_unix, created_at_unix, attempt
FROM dispatches WHERE id LIKE ? || '%' ORDER BY created_at_unix DESC
`
	rows, err := d.conn.QueryContext(ctx, q, planPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dispatch
	for rows.Next() {
		var r Dispatch
		if err := rows.Scan(&r.ID, &r.Vendor, &r.Recipient, &r.Subject, &r.Status, &r.Error,
			&r.SentAtUnix, &r.CreatedUnix, &r.Attempt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountByVendor returns the count of dispatches for each vendor (for observability).
func (d *DB) CountByVendor(ctx context.Context) (map[string]int, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT vendor, COUNT(*) FROM dispatches GROUP BY vendor`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var v string
		var c int
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v] = c
	}
	return out, rows.Err()
}
