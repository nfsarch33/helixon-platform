// Package notifydb — v17607-6 LRU sender tracking.
//
// RotatingSender.LeastRecentlyUsed() picks the vendor key whose
// last_used_at is the oldest. This keeps every key "warm" (vendor
// inactivity-deletion policies expire keys after ~90 days; rotating
// keeps the warmest key under the cap).
//
// Schema: key_uses(vendor TEXT, key_id TEXT, last_used_unix INTEGER,
// PRIMARY KEY (vendor, key_id)).
package notifydb

import (
	"context"
	"errors"
	"time"
)

// KeyUse is the LRU row tracked per (vendor, key_id).
type KeyUse struct {
	Vendor       string `json:"vendor"`
	KeyID        string `json:"key_id"`
	LastUsedUnix int64  `json:"last_used_unix"`
}

// migrateKeyUses adds the key_uses table if it doesn't already exist.
// Idempotent — safe to run on every Open.
func (d *DB) migrateKeyUses() error {
	const schema = `
CREATE TABLE IF NOT EXISTS key_uses (
    vendor TEXT NOT NULL,
    key_id TEXT NOT NULL,
    last_used_unix INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (vendor, key_id)
);
CREATE INDEX IF NOT EXISTS idx_key_uses_vendor_lru ON key_uses(vendor, last_used_unix);
`
	_, err := d.conn.Exec(schema)
	return err
}

// RecordKeyUse upserts a (vendor, key_id) row with the supplied
// LastUsedUnix. If the row already exists, last_used_unix is updated.
func (d *DB) RecordKeyUse(ctx context.Context, ku KeyUse) error {
	if ku.Vendor == "" || ku.KeyID == "" {
		return errors.New("notifydb.RecordKeyUse: Vendor and KeyID required")
	}
	if ku.LastUsedUnix == 0 {
		ku.LastUsedUnix = time.Now().Unix()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.ExecContext(ctx, `
INSERT INTO key_uses (vendor, key_id, last_used_unix)
VALUES (?, ?, ?)
ON CONFLICT (vendor, key_id) DO UPDATE SET last_used_unix = excluded.last_used_unix
`, ku.Vendor, ku.KeyID, ku.LastUsedUnix)
	if err != nil {
		return err
	}
	return nil
}

// ListKeyUses returns all (vendor, key_id) rows for a vendor, sorted
// by last_used_unix DESC (most-recently-used first). The caller can
// then call PickLeastRecentlyUsed on the LAST element to get the LRU
// pick, or sort the list explicitly.
func (d *DB) ListKeyUses(ctx context.Context, vendor string) ([]KeyUse, error) {
	if vendor == "" {
		return nil, errors.New("notifydb.ListKeyUses: vendor required")
	}
	rows, err := d.conn.QueryContext(ctx, `
SELECT vendor, key_id, last_used_unix FROM key_uses WHERE vendor = ? ORDER BY last_used_unix DESC
`, vendor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyUse
	for rows.Next() {
		var ku KeyUse
		if err := rows.Scan(&ku.Vendor, &ku.KeyID, &ku.LastUsedUnix); err != nil {
			return nil, err
		}
		out = append(out, ku)
	}
	return out, rows.Err()
}

// PickLeastRecentlyUsed returns the KeyUse with the smallest
// LastUsedUnix — the key we should use next to keep all keys warm.
func PickLeastRecentlyUsed(keys []KeyUse) (KeyUse, error) {
	if len(keys) == 0 {
		return KeyUse{}, errors.New("notifydb.PickLeastRecentlyUsed: empty list")
	}
	lru := keys[0]
	for _, k := range keys[1:] {
		if k.LastUsedUnix < lru.LastUsedUnix {
			lru = k
		}
	}
	return lru, nil
}

// Ensure migrateKeyUses runs on Open — call from db.go's migrate().
func init() {
	// No-op at package level; db.go's migrate() invokes it explicitly
	// to keep migration ordering explicit and discoverable.
}
