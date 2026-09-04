package alertnotifier

import (
	"strings"
	"testing"
	"time"
)

func TestSubject(t *testing.T) {
	t.Parallel()
	a := fixtureAlert("A", "critical", "c5", "1")
	b := fixtureAlert("B", "warning", "c7", "2")
	tests := []struct {
		name string
		ch   Change
		want string
	}{
		{name: "new only", ch: Change{NewFiring: []Alert{a, b}}, want: "[HELIXON] 2 new / 0 resolved"},
		{name: "new and resolved", ch: Change{NewFiring: []Alert{a}, Resolved: []Entry{{Fingerprint: "x"}}}, want: "[HELIXON] 1 new / 1 resolved"},
		{name: "includes still firing", ch: Change{NewFiring: []Alert{a}, Renotify: []Alert{b}}, want: "[HELIXON] 1 new / 0 resolved / 1 still firing"},
		{name: "resolved only", ch: Change{Resolved: []Entry{{Fingerprint: "x"}, {Fingerprint: "y"}}}, want: "[HELIXON] 0 new / 2 resolved"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Subject(tc.ch); got != tc.want {
				t.Fatalf("Subject = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderBody(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	crit := fixtureAlert("TailscaleNodeDown", "critical", "c5", "43d404bfbb8ccf1c")
	warn := fixtureAlert("FleetDoctorRed", "warning", "127.0.0.1:9100", "0778352592826b0a")
	resolved := Entry{
		Fingerprint: "gone-1", AlertName: "SystemdUnitFailed", Severity: "warning",
		Labels:   map[string]string{"alertname": "SystemdUnitFailed", "severity": "warning", "unit": "llm-router.service"},
		StartsAt: time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
	}

	ch := Change{NewFiring: []Alert{crit, warn}, Resolved: []Entry{resolved}}
	d := Render(ch, NewState(), now, "http://127.0.0.1:9093/")

	mustContain := []string{
		"NEW FIRING (2)",
		"RESOLVED (1)",
		"severity=critical",
		"severity=warning",
		"TailscaleNodeDown",
		"FleetDoctorRed",
		"SystemdUnitFailed",
		"instance=c5",
		"unit=llm-router.service",
		"2026-08-27T14:12:18Z",
		"2026-08-27T09:00:00Z",
		"http://127.0.0.1:9093/api/v2/alerts",
		// The header is dated by the cycle, no longer the render clock: it is
		// the newest instant the idempotency key already folds in, which here
		// is the critical alert's StartsAt. It used to be `now`
		// (2026-08-28T02:00:00Z), and that is precisely what made a retry
		// re-render a different body under the same key and earn a vendor
		// 409 invalid_idempotent_request -- see
		// TestRenderIsStableAcrossTheRetryClock.
		"Helixon fleet alert digest — 2026-08-27T14:12:18Z",
	}
	for _, want := range mustContain {
		if !strings.Contains(d.Text, want) {
			t.Errorf("digest text missing %q:\n%s", want, d.Text)
		}
	}
	// The render clock must not appear in the body at all. If it does, a
	// retry of this same cycle renders different bytes under the same
	// idempotency key, which the vendor rejects with 409.
	if strings.Contains(d.Text, "2026-08-28T02:00:00Z") {
		t.Errorf("the render clock leaked into the idempotent body:\n%s", d.Text)
	}

	// Critical must be rendered before warning.
	if strings.Index(d.Text, "severity=critical") > strings.Index(d.Text, "severity=warning") {
		t.Errorf("critical must precede warning:\n%s", d.Text)
	}
	// An empty section must be omitted, not printed as "(0)".
	if strings.Contains(d.Text, "STILL FIRING") {
		t.Errorf("empty renotify section should be omitted:\n%s", d.Text)
	}
	if !strings.Contains(d.HTML, "<pre") || !strings.Contains(d.HTML, "TailscaleNodeDown") {
		t.Errorf("HTML body malformed:\n%s", d.HTML)
	}
	if d.Subject != "[HELIXON] 2 new / 1 resolved" {
		t.Errorf("Subject = %q", d.Subject)
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	t.Parallel()
	evil := Alert{
		Labels:      map[string]string{"alertname": "XSS", "severity": "critical", "unit": "<script>alert(1)</script>"},
		Status:      AlertStatus{State: "active"},
		Fingerprint: "evil",
	}
	d := Render(Change{NewFiring: []Alert{evil}}, NewState(), time.Now(), DefaultAlertmanagerURL)
	if strings.Contains(d.HTML, "<script>") {
		t.Fatalf("label content was not escaped:\n%s", d.HTML)
	}
	if !strings.Contains(d.HTML, "&lt;script&gt;") {
		t.Fatalf("expected escaped markup:\n%s", d.HTML)
	}
}

func TestIdempotencyKey(t *testing.T) {
	t.Parallel()
	a := fixtureAlert("A", "critical", "c5", "fp-a")
	b := fixtureAlert("B", "warning", "c7", "fp-b")

	base := Change{NewFiring: []Alert{a}}
	same := Change{NewFiring: []Alert{a}}
	bigger := Change{NewFiring: []Alert{a, b}}
	resolvedInstead := Change{Resolved: []Entry{{Fingerprint: "fp-a"}}}

	if IdempotencyKey(base, NewState()) != IdempotencyKey(same, NewState()) {
		t.Error("identical change sets must reuse the key so a retry cannot double-send")
	}
	if IdempotencyKey(base, NewState()) == IdempotencyKey(bigger, NewState()) {
		t.Error("a different change set must produce a different key")
	}
	if IdempotencyKey(base, NewState()) == IdempotencyKey(resolvedInstead, NewState()) {
		t.Error("firing and resolving the same alert are different messages")
	}
	if !strings.HasPrefix(IdempotencyKey(base, NewState()), idempotencyPrefix) {
		t.Error("key must be namespaced")
	}
	// Order of arrival must not change the key.
	shuffled := Change{NewFiring: []Alert{b, a}}
	if IdempotencyKey(bigger, NewState()) != IdempotencyKey(shuffled, NewState()) {
		t.Error("key must not depend on slice order")
	}
}

func TestIdempotencyKeyDistinguishesRenotifyCycles(t *testing.T) {
	t.Parallel()
	a := fixtureAlert("A", "critical", "c5", "fp-a")
	ch := Change{Renotify: []Alert{a}}

	cycle1 := NewState()
	cycle1.Active["fp-a"] = Entry{Fingerprint: "fp-a", LastNotifiedUnix: 1000}
	cycle2 := NewState()
	cycle2.Active["fp-a"] = Entry{Fingerprint: "fp-a", LastNotifiedUnix: 1000 + int64(DefaultRenotifyInterval.Seconds())}

	k1 := IdempotencyKey(ch, cycle1)
	k2 := IdempotencyKey(ch, cycle2)
	if k1 == k2 {
		t.Fatal("consecutive renotify cycles must not be de-duplicated into one email")
	}
	if k1 != IdempotencyKey(ch, cycle1) {
		t.Fatal("a retry within one cycle must reuse the key")
	}
}

func TestGroupSeverities(t *testing.T) {
	t.Parallel()
	alerts := []Alert{
		fixtureAlert("A", "info", "c5", "1"),
		fixtureAlert("B", "critical", "c5", "2"),
		fixtureAlert("C", "warning", "c5", "3"),
		fixtureAlert("D", "critical", "c7", "4"),
		{Labels: map[string]string{"alertname": "E"}},
	}
	got := groupSeverities(alerts)
	want := []string{"critical", "warning", "info", severityUnknown}
	if len(got) != len(want) {
		t.Fatalf("groupSeverities = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("groupSeverities = %v, want %v", got, want)
		}
	}
}

// A retry must re-render byte-identical text.
//
// This is the bug the frozen-clock retry test could not see: run_test.go pins
// "the retry resends the identical digest" while holding the clock still, so a
// body that varies with wall-clock time passes it. Production advances the
// clock between the failed send and the retry, which is what produced
// 409 invalid_idempotent_request twice on 2026-09-05.
//
// The assertion is the pair: same key AND same bytes. Either alone is passable
// by a broken implementation - a body-derived key would keep the bytes stable
// and change the key, which loses vendor de-duplication.
func TestRenderIsStableAcrossTheRetryClock(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 5, 5, 30, 0, 0, time.UTC)
	ch := Change{
		NewFiring: []Alert{{
			Labels:   map[string]string{"alertname": "AgentSandboxFailing", "severity": "critical"},
			StartsAt: start,
		}},
	}
	prev := State{Active: map[string]Entry{}}

	first := Render(ch, prev, start.Add(30*time.Minute), "http://example.invalid")
	// The retry happens five minutes later, exactly as the unit's restart does.
	retry := Render(ch, prev, start.Add(35*time.Minute), "http://example.invalid")

	if first.IdempotencyKey != retry.IdempotencyKey {
		t.Fatalf("the retry changed the idempotency key: %q -> %q", first.IdempotencyKey, retry.IdempotencyKey)
	}
	if first.Text != retry.Text {
		t.Fatalf("the retry re-rendered a different body under the same key;\nfirst:\n%s\nretry:\n%s", first.Text, retry.Text)
	}
	if first.HTML != retry.HTML {
		t.Fatal("the retry re-rendered different HTML under the same key")
	}
	if first.Subject != retry.Subject {
		t.Fatalf("the retry changed the subject: %q -> %q", first.Subject, retry.Subject)
	}
}

// The control: a genuinely new cycle must still produce a new key, or the
// stability above would have been bought by making every digest identical.
func TestRenderStillDistinguishesANewCycle(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 5, 5, 30, 0, 0, time.UTC)
	a := Alert{Labels: map[string]string{"alertname": "AgentSandboxFailing"}, StartsAt: start}
	fp := FingerprintOf(&a)

	first := Render(Change{Renotify: []Alert{a}},
		State{Active: map[string]Entry{fp: {LastNotifiedUnix: start.Unix()}}},
		start.Add(time.Hour), "http://example.invalid")
	second := Render(Change{Renotify: []Alert{a}},
		State{Active: map[string]Entry{fp: {LastNotifiedUnix: start.Add(time.Hour).Unix()}}},
		start.Add(2*time.Hour), "http://example.invalid")

	if first.IdempotencyKey == second.IdempotencyKey {
		t.Fatal("a later renotify cycle reused the key; it is genuinely a new message")
	}
	if first.Text == second.Text {
		t.Fatal("a later renotify cycle rendered an identical body; the header no longer tracks the cycle")
	}
}
