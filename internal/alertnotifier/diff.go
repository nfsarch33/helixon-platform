package alertnotifier

import (
	"sort"
	"time"
)

// DefaultRenotifyInterval is how long a still-firing alert stays quiet
// before it is repeated. Six hours is chosen so a long-lived alert reaches
// a human a few times a day without training them to ignore the sender.
const DefaultRenotifyInterval = 6 * time.Hour

// Change is the delta one run found between the remembered state and the
// live Alertmanager list.
type Change struct {
	// NewFiring are alerts that were not firing at the end of the last
	// delivered run.
	NewFiring []Alert
	// Renotify are alerts that were already firing and whose renotify
	// interval has elapsed.
	Renotify []Alert
	// Resolved are remembered alerts that Alertmanager no longer lists.
	Resolved []Entry
}

// Empty reports whether there is nothing worth an email. A run that sees
// no change sends nothing and exits 0 — silence is the correct output.
func (c Change) Empty() bool {
	return len(c.NewFiring) == 0 && len(c.Renotify) == 0 && len(c.Resolved) == 0
}

// severityRank orders severities most-urgent-first for rendering. Unknown
// severities sort after the known ones but keep a stable order among
// themselves via the alphabetical tiebreak in the sort comparators.
func severityRank(sev string) int {
	switch sev {
	case "critical", "page":
		return 0
	case "warning", "warn":
		return 1
	case "info":
		return 2
	default:
		return 3
	}
}

// Diff compares the remembered state against the live alert list and
// returns both the change to report and the state to persist once that
// report has actually been delivered.
//
// The returned state optimistically stamps LastNotifiedUnix = now for
// every alert included in this report. The caller MUST NOT persist it
// unless the send succeeded; on a failed send the old state is kept so the
// next run retries the same report rather than swallowing it.
func Diff(prev State, alerts []Alert, now time.Time, renotify time.Duration) (Change, State) {
	if renotify <= 0 {
		renotify = DefaultRenotifyInterval
	}
	next := NewState()
	next.LastRunUnix = prev.LastRunUnix
	next.LastSuccessUnix = prev.LastSuccessUnix

	var ch Change
	seen := make(map[string]struct{}, len(alerts))
	for i := range alerts {
		a := &alerts[i]
		fp := FingerprintOf(a)
		seen[fp] = struct{}{}
		entry := newEntry(fp, a, now)
		old, known := prev.Active[fp]
		switch {
		case !known:
			ch.NewFiring = append(ch.NewFiring, *a)
		default:
			entry.FirstSeenUnix = old.FirstSeenUnix
			entry.LastNotifiedUnix = old.LastNotifiedUnix
			if renotifyDue(old.LastNotifiedUnix, now, renotify) {
				ch.Renotify = append(ch.Renotify, *a)
				entry.LastNotifiedUnix = now.Unix()
			}
		}
		next.Active[fp] = entry
	}
	for fp, old := range prev.Active {
		if _, still := seen[fp]; !still {
			ch.Resolved = append(ch.Resolved, old)
		}
	}
	sortChange(&ch)
	return ch, next
}

// newEntry builds the state entry for a currently-firing alert, stamped as
// notified now (the caller only persists it after a successful send).
func newEntry(fp string, a *Alert, now time.Time) Entry {
	return Entry{
		Fingerprint:      fp,
		AlertName:        a.Name(),
		Severity:         a.Severity(),
		Labels:           a.Labels,
		StartsAt:         a.StartsAt,
		FirstSeenUnix:    now.Unix(),
		LastNotifiedUnix: now.Unix(),
	}
}

// renotifyDue reports whether a still-firing alert has been quiet long
// enough to repeat. A zero LastNotifiedUnix (an entry written by an older
// schema) is treated as due so the alert is never permanently muted.
func renotifyDue(lastNotifiedUnix int64, now time.Time, renotify time.Duration) bool {
	if lastNotifiedUnix <= 0 {
		return true
	}
	return now.Sub(time.Unix(lastNotifiedUnix, 0)) >= renotify
}

// sortChange imposes a deterministic order on every section so the
// rendered digest and the derived idempotency key are stable across runs
// (Go map iteration is not).
func sortChange(ch *Change) {
	byAlert := func(s []Alert) {
		sort.Slice(s, func(i, j int) bool {
			return alertLess(&s[i], &s[j])
		})
	}
	byAlert(ch.NewFiring)
	byAlert(ch.Renotify)
	sort.Slice(ch.Resolved, func(i, j int) bool {
		a, b := ch.Resolved[i], ch.Resolved[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.AlertName != b.AlertName {
			return a.AlertName < b.AlertName
		}
		return a.Fingerprint < b.Fingerprint
	})
}

// alertLess orders alerts by severity, then name, then fingerprint.
func alertLess(a, b *Alert) bool {
	if ra, rb := severityRank(a.Severity()), severityRank(b.Severity()); ra != rb {
		return ra < rb
	}
	if a.Name() != b.Name() {
		return a.Name() < b.Name()
	}
	return FingerprintOf(a) < FingerprintOf(b)
}
