// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
// Package notify — v17607-6 recipient unification.
//
// Canonical recipient allowlist (per v16101 + CARRY-044):
//
//	jaslian@gmail.com — only
//
// Historical CC addresses (info@oztac.com.au, info@cylrl.com.au) are
// dropped; they were legacy multi-tenant aliases that no longer
// reflect the operator's single-identity posture (v14502 identity
// correction + DRL v8.5 rule-41 notify-frequency-jaslian-only-session-close).
package notify

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNonCanonicalRecipient is returned by ValidateRecipients (and
// propagated by Dispatcher.Send) when an Email specifies any recipient
// outside the canonical allowlist. The error is permanent: do not retry.
var ErrNonCanonicalRecipient = errors.New("non-canonical recipient (only jaslian@gmail.com allowed)")

// canonicalTarget is the single allowed notification target. Defined
// as a package var so future tests can reference the canonical value
// without depending on a hard-coded literal.
var canonicalTarget = "jaslian@gmail.com"

// CanonicalTargets returns the canonical recipient allowlist. Length
// is always 1 (single canonical target).
func CanonicalTargets() []string {
	return []string{canonicalTarget}
}

// ValidateRecipients returns ErrNonCanonicalRecipient if any recipient
// in the input is not in the canonical allowlist. The check is exact
// (case-insensitive) — partial matches and substring matches are
// rejected to avoid accidental wildcard-like behaviour.
func ValidateRecipients(recipients []string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("%w: no recipients supplied", ErrNonCanonicalRecipient)
	}
	for _, r := range recipients {
		if !strings.EqualFold(strings.TrimSpace(r), canonicalTarget) {
			return fmt.Errorf("%w: %q", ErrNonCanonicalRecipient, r)
		}
	}
	return nil
}
