// Package notify — v17607-6 RotatingSender (LRU strategy).
//
// RotatingSender picks the next vendor key to use by querying notifydb
// for the (vendor, key_id) row with the OLDEST last_used_unix. This
// keeps every key warm — vendor inactivity-deletion policies delete
// keys unused for ~90 days; rotating on every send keeps all keys
// safely under the cap.
//
// Wiring:
//
//	resend1 := notify.NewResendClient(notify.ResendConfig{APIKey: "re_key1"})
//	resend2 := notify.NewResendClient(notify.ResendConfig{APIKey: "re_key2"})
//	brevo  := notify.NewBrevoClient(notify.BrevoConfig{APIKey: "xkeysib_1"})
//	rs := notify.NewRotatingSender(notifydb.DB, []notify.Client{resend1, resend2, brevo})
//	err := rs.Send(ctx, m)
//
// The mapping from "Client" to "(vendor, key_id)" comes from
// Client.Vendor() and a static key-id table supplied at construction
// time. The first iteration uses a small NewRotatingSenderWithKeys
// helper that takes the key IDs explicitly so tests can verify
// behaviour without depending on environment variables.
package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

// ErrNoClients is returned when RotatingSender is constructed with
// an empty clients list.
var ErrNoClients = errors.New("rotating sender: no clients configured")

// RotatingSender is a Client that picks the LRU vendor key from the
// supplied pool, sends via that key, and records the use.
type RotatingSender struct {
	mu      sync.Mutex
	db      *notifydb.DB
	clients []Client
	keyIDs  []string // parallel to clients; keyIDs[i] is the key_id for clients[i]
}

// NewRotatingSender wires a RotatingSender to the supplied notifydb.
// clients and keyIDs must be the same length; keyIDs[i] is the
// 1Password item ID (or equivalent) for clients[i].
func NewRotatingSender(db *notifydb.DB, clients []Client, keyIDs []string) (*RotatingSender, error) {
	if len(clients) == 0 {
		return nil, ErrNoClients
	}
	if len(clients) != len(keyIDs) {
		return nil, fmt.Errorf("rotating sender: clients/keyIDs length mismatch (%d vs %d)", len(clients), len(keyIDs))
	}
	for _, k := range keyIDs {
		if k == "" {
			return nil, errors.New("rotating sender: empty key_id")
		}
	}
	return &RotatingSender{db: db, clients: clients, keyIDs: keyIDs}, nil
}

// Send dispatches via the LRU-picked client and records the use in
// notifydb. The first send-after-restart is the row with the smallest
// last_used_unix (or a freshly-initialised row of zero if no usage
// has ever been recorded for the vendor).
func (r *RotatingSender) Send(ctx context.Context, m Email) error {
	if m.IdempotencyKey == "" {
		return fmt.Errorf("%w: IdempotencyKey required", ErrPermanent)
	}
	if err := ValidateRecipients(m.To); err != nil {
		return err
	}

	pick, err := r.pickLRU(ctx)
	if err != nil {
		return err
	}
	client := r.clients[pick.idx]

	// Dispatch through the picked client.
	sendErr := client.Send(ctx, m)

	// Record the use regardless of outcome — successful OR failed sends
	// both count as "use" for inactivity-deletion policy purposes. This
	// matches the intent of warm-ping: keep keys active.
	if r.db != nil {
		_ = r.db.RecordKeyUse(ctx, notifydb.KeyUse{
			Vendor:       client.Vendor(),
			KeyID:        r.keyIDs[pick.idx],
			LastUsedUnix: time.Now().Unix(),
		})
	}
	return sendErr
}

// Vendor returns the vendor of the currently-picked client. Useful
// for cost attribution / audit events.
func (r *RotatingSender) Vendor() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.clients) == 0 {
		return ""
	}
	return r.clients[0].Vendor()
}

// lruPick is the index into r.clients that was picked.
type lruPick struct {
	idx int
}

// pickLRU returns the index of the LRU key. If no usage has ever been
// recorded for any of the supplied keys, returns 0 (deterministic).
func (r *RotatingSender) pickLRU(ctx context.Context) (lruPick, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.db == nil {
		// Without a DB we have no LRU signal — fall back to index 0.
		return lruPick{idx: 0}, nil
	}

	// Group clients by vendor and pick the oldest key per vendor.
	// For simplicity, treat all clients as a flat list and pick
	// the single oldest.
	now := time.Now().Unix()
	type candidate struct {
		idx     int
		lastUse int64
	}
	cands := make([]candidate, len(r.clients))
	for i := range r.clients {
		vendor := r.clients[i].Vendor()
		uses, err := r.db.ListKeyUses(ctx, vendor)
		if err != nil {
			return lruPick{}, fmt.Errorf("rotating sender: list key uses: %w", err)
		}
		// Find this specific key_id (or default to epoch if unseen).
		cands[i] = candidate{idx: i, lastUse: 0}
		for _, u := range uses {
			if u.KeyID == r.keyIDs[i] {
				cands[i].lastUse = u.LastUsedUnix
				break
			}
		}
	}

	// Pick the candidate with the smallest lastUse; tie-break by index
	// for determinism.
	best := cands[0]
	for _, c := range cands[1:] {
		if c.lastUse < best.lastUse {
			best = c
		}
	}
	_ = now
	return lruPick{idx: best.idx}, nil
}
