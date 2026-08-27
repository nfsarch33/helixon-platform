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
		"2026-08-28T02:00:00Z",
	}
	for _, want := range mustContain {
		if !strings.Contains(d.Text, want) {
			t.Errorf("digest text missing %q:\n%s", want, d.Text)
		}
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
