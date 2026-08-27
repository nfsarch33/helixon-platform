package alertnotifier

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SubjectPrefix tags every digest so a mail rule can file them.
const SubjectPrefix = "[HELIXON]"

// idempotencyPrefix namespaces this component's idempotency keys inside
// the shared notify vendor key space.
const idempotencyPrefix = "alert-notifier-"

// Digest is one rendered email plus the idempotency key that makes
// re-sending it safe.
type Digest struct {
	Subject        string
	Text           string
	HTML           string
	IdempotencyKey string
}

// Render builds the digest for a change set.
//
// prev is needed to date the renotify entries: the idempotency key folds
// in each still-firing alert's previous notification timestamp, so a retry
// of the same cycle reuses the key (the vendor de-duplicates it) while the
// next renotify cycle produces a different one (it is genuinely a new
// message).
func Render(ch Change, prev State, now time.Time, source string) Digest {
	var b strings.Builder
	fmt.Fprintf(&b, "Helixon fleet alert digest — %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Source: %s%s\n\n", strings.TrimRight(source, "/"), alertsPath)

	writeAlertSection(&b, fmt.Sprintf("NEW FIRING (%d)", len(ch.NewFiring)), ch.NewFiring)
	writeResolvedSection(&b, fmt.Sprintf("RESOLVED (%d)", len(ch.Resolved)), ch.Resolved)
	writeAlertSection(&b, fmt.Sprintf("STILL FIRING (%d)", len(ch.Renotify)), ch.Renotify)

	text := b.String()
	return Digest{
		Subject:        Subject(ch),
		Text:           text,
		HTML:           renderHTML(text),
		IdempotencyKey: IdempotencyKey(ch, prev),
	}
}

// Subject summarizes the change set in the mail subject, so an operator
// glancing at a phone lock screen already knows the shape of it.
func Subject(ch Change) string {
	s := fmt.Sprintf("%s %d new / %d resolved", SubjectPrefix, len(ch.NewFiring), len(ch.Resolved))
	if len(ch.Renotify) > 0 {
		s += fmt.Sprintf(" / %d still firing", len(ch.Renotify))
	}
	return s
}

// writeAlertSection renders one severity-grouped block of live alerts.
// Empty sections are omitted rather than printed as "(0)" noise.
func writeAlertSection(b *strings.Builder, heading string, alerts []Alert) {
	if len(alerts) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n%s\n", heading, strings.Repeat("-", len(heading)))
	for _, sev := range groupSeverities(alerts) {
		fmt.Fprintf(b, "  severity=%s\n", sev)
		for i := range alerts {
			a := &alerts[i]
			if a.Severity() != sev {
				continue
			}
			fmt.Fprintf(b, "    %s\n", a.Name())
			if labels := a.DistinguishingLabels(); len(labels) > 0 {
				fmt.Fprintf(b, "      labels:   %s\n", strings.Join(labels, ", "))
			}
			fmt.Fprintf(b, "      startsAt: %s\n", a.StartsAt.UTC().Format(time.RFC3339))
			if summary := strings.TrimSpace(a.Annotations["summary"]); summary != "" {
				fmt.Fprintf(b, "      summary:  %s\n", summary)
			}
		}
	}
	b.WriteString("\n")
}

// writeResolvedSection renders the remembered alerts Alertmanager dropped.
func writeResolvedSection(b *strings.Builder, heading string, entries []Entry) {
	if len(entries) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n%s\n", heading, strings.Repeat("-", len(heading)))
	for _, e := range entries {
		fmt.Fprintf(b, "    [%s] %s\n", e.Severity, e.AlertName)
		if labels := distinguishing(e.Labels); len(labels) > 0 {
			fmt.Fprintf(b, "      labels:   %s\n", strings.Join(labels, ", "))
		}
		fmt.Fprintf(b, "      startsAt: %s\n", e.StartsAt.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n")
}

// distinguishing mirrors Alert.DistinguishingLabels for a stored Entry.
func distinguishing(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == labelAlertname || k == labelSeverity {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+labels[k])
	}
	return out
}

// groupSeverities returns the distinct severities present, most urgent
// first, so critical alerts are never buried below warnings.
func groupSeverities(alerts []Alert) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(alerts))
	for i := range alerts {
		sev := alerts[i].Severity()
		if _, ok := seen[sev]; ok {
			continue
		}
		seen[sev] = struct{}{}
		out = append(out, sev)
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := severityRank(out[i]), severityRank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// renderHTML wraps the plain-text digest in escaped preformatted HTML.
// The text form is authoritative; the HTML exists only so mail clients
// that prefer text/html still show a monospaced, aligned digest.
func renderHTML(text string) string {
	return "<html><body><pre style=\"font-family:ui-monospace,Menlo,Consolas,monospace;font-size:13px\">" +
		html.EscapeString(text) +
		"</pre></body></html>"
}

// IdempotencyKey derives a key from exactly the set of changes being
// reported, so a retry after a transient vendor failure cannot produce a
// second email while a genuinely different change set always can.
func IdempotencyKey(ch Change, prev State) string {
	parts := make([]string, 0, len(ch.NewFiring)+len(ch.Renotify)+len(ch.Resolved))
	for i := range ch.NewFiring {
		parts = append(parts, "new:"+FingerprintOf(&ch.NewFiring[i]))
	}
	for i := range ch.Renotify {
		fp := FingerprintOf(&ch.Renotify[i])
		// Fold in the previous notification stamp so consecutive renotify
		// cycles for an unchanged alert set are distinct messages.
		parts = append(parts, "renotify:"+fp+"@"+strconv.FormatInt(prev.Active[fp].LastNotifiedUnix, 10))
	}
	for _, e := range ch.Resolved {
		parts = append(parts, "resolved:"+e.Fingerprint)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return idempotencyPrefix + hex.EncodeToString(sum[:])[:32]
}
