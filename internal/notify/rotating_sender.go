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

// RotatingSenderMode controls which vendors are eligible for LRU
// selection. The default mode is LRUAll (existing behaviour). The
// BrevoOnly mode is used when Resend's sender-domain is unverified
// (CF-105), per xcut-10 (v18518) — Resend keys are skipped entirely.
type RotatingSenderMode int

const (
	// RotatingSenderModeLRUAll picks the oldest key across all
	// configured vendors (Resend + Brevo).
	RotatingSenderModeLRUAll RotatingSenderMode = iota
	// RotatingSenderModeBrevoOnly restricts the LRU pick to Brevo
	// clients only; Resend clients are skipped but still recorded
	// for future use once the domain is verified.
	RotatingSenderModeBrevoOnly
)

// RotatingSender is a Client that picks the LRU vendor key from the
// supplied pool, sends via that key, and records the use.
type RotatingSender struct {
	mu      sync.Mutex
	db      *notifydb.DB
	clients []Client
	keyIDs  []string // parallel to clients; keyIDs[i] is the key_id for clients[i]
	mode    RotatingSenderMode
}

// NewRotatingSender wires a RotatingSender to the supplied notifydb.
// clients and keyIDs must be the same length; keyIDs[i] is the
// 1Password item ID (or equivalent) for clients[i]. Equivalent to
// NewRotatingSenderWithMode(db, clients, keyIDs, RotatingSenderModeLRUAll).
func NewRotatingSender(db *notifydb.DB, clients []Client, keyIDs []string) (*RotatingSender, error) {
	return NewRotatingSenderWithMode(db, clients, keyIDs, RotatingSenderModeLRUAll)
}

// NewRotatingSenderWithMode wires a RotatingSender with an explicit
// vendor-eligibility mode. Use RotatingSenderModeBrevoOnly when the
// Resend sender domain is unverified (CF-105) — Resend clients are
// dropped from the LRU pool.
func NewRotatingSenderWithMode(db *notifydb.DB, clients []Client, keyIDs []string, mode RotatingSenderMode) (*RotatingSender, error) {
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
	if mode == RotatingSenderModeBrevoOnly {
		hasBrevo := false
		for _, c := range clients {
			if c.Vendor() == "brevo" {
				hasBrevo = true
				break
			}
		}
		if !hasBrevo {
			return nil, errors.New("rotating sender: BrevoOnly mode requires at least one Brevo client")
		}
	}
	return &RotatingSender{db: db, clients: clients, keyIDs: keyIDs, mode: mode}, nil
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
//
// xcut-10 (v18518): in RotatingSenderModeBrevoOnly, Resend clients are
// filtered out of the candidate pool. The filter is applied BEFORE the
// LRU comparison so the rotation naturally exercises every Brevo key.
func (r *RotatingSender) pickLRU(ctx context.Context) (lruPick, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Apply vendor eligibility filter based on mode.
	eligible := make([]int, 0, len(r.clients))
	for i, c := range r.clients {
		switch r.mode {
		case RotatingSenderModeBrevoOnly:
			if c.Vendor() != "brevo" {
				continue
			}
		case RotatingSenderModeLRUAll:
			// fall through
		}
		eligible = append(eligible, i)
	}
	if len(eligible) == 0 {
		return lruPick{}, errors.New("rotating sender: no eligible clients for current mode")
	}

	if r.db == nil {
		// Without a DB we have no LRU signal — fall back to the first
		// eligible index.
		return lruPick{idx: eligible[0]}, nil
	}

	type candidate struct {
		idx     int
		lastUse int64
	}
	cands := make([]candidate, 0, len(eligible))
	for _, i := range eligible {
		vendor := r.clients[i].Vendor()
		uses, err := r.db.ListKeyUses(ctx, vendor)
		if err != nil {
			return lruPick{}, fmt.Errorf("rotating sender: list key uses: %w", err)
		}
		c := candidate{idx: i, lastUse: 0}
		for _, u := range uses {
			if u.KeyID == r.keyIDs[i] {
				c.lastUse = u.LastUsedUnix
				break
			}
		}
		cands = append(cands, c)
	}

	// Pick the candidate with the smallest lastUse; tie-break by index
	// for determinism.
	best := cands[0]
	for _, c := range cands[1:] {
		if c.lastUse < best.lastUse {
			best = c
		}
	}
	return lruPick{idx: best.idx}, nil
}
