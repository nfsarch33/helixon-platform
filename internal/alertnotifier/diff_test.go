package alertnotifier

import (
	"testing"
	"time"
)

// stateWith builds a prior state whose entries were all notified at
// notifiedAt.
func stateWith(notifiedAt time.Time, alerts ...Alert) State {
	st := NewState()
	for i := range alerts {
		a := &alerts[i]
		fp := FingerprintOf(a)
		st.Active[fp] = Entry{
			Fingerprint:      fp,
			AlertName:        a.Name(),
			Severity:         a.Severity(),
			Labels:           a.Labels,
			StartsAt:         a.StartsAt,
			FirstSeenUnix:    notifiedAt.Unix(),
			LastNotifiedUnix: notifiedAt.Unix(),
		}
	}
	return st
}

func TestDiff(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	tailscale := fixtureAlert("TailscaleNodeDown", "critical", "c5", "43d404bfbb8ccf1c")
	router := fixtureAlert("RouterFleetNodeUnhealthy", "warning", "c7", "5897034939b701ca")
	doctor := fixtureAlert("FleetDoctorRed", "warning", "127.0.0.1:9100", "0778352592826b0a")

	tests := []struct {
		name         string
		prev         State
		alerts       []Alert
		renotify     time.Duration
		wantNew      int
		wantResolved int
		wantRenotify int
		wantEmpty    bool
	}{
		{
			name:      "first run reports every firing alert as new",
			prev:      NewState(),
			alerts:    []Alert{tailscale, router, doctor},
			wantNew:   3,
			wantEmpty: false,
		},
		{
			name:      "no change sends nothing",
			prev:      stateWith(now.Add(-time.Hour), tailscale, router),
			alerts:    []Alert{tailscale, router},
			wantEmpty: true,
		},
		{
			name:         "newly firing alongside known alerts",
			prev:         stateWith(now.Add(-time.Hour), tailscale),
			alerts:       []Alert{tailscale, router},
			wantNew:      1,
			wantResolved: 0,
		},
		{
			name:         "newly resolved is detected",
			prev:         stateWith(now.Add(-time.Hour), tailscale, router),
			alerts:       []Alert{tailscale},
			wantResolved: 1,
		},
		{
			name:      "all resolved",
			prev:      stateWith(now.Add(-time.Hour), tailscale, router),
			alerts:    nil,
			wantEmpty: false, wantResolved: 2,
		},
		{
			name:      "renotify suppressed inside the window",
			prev:      stateWith(now.Add(-5*time.Hour), tailscale),
			alerts:    []Alert{tailscale},
			renotify:  6 * time.Hour,
			wantEmpty: true,
		},
		{
			name:         "renotify fires once the window elapses",
			prev:         stateWith(now.Add(-7*time.Hour), tailscale),
			alerts:       []Alert{tailscale},
			renotify:     6 * time.Hour,
			wantRenotify: 1,
		},
		{
			name:         "renotify fires exactly on the boundary",
			prev:         stateWith(now.Add(-6*time.Hour), tailscale),
			alerts:       []Alert{tailscale},
			renotify:     6 * time.Hour,
			wantRenotify: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ch, next := Diff(tc.prev, tc.alerts, now, tc.renotify)
			if len(ch.NewFiring) != tc.wantNew {
				t.Errorf("NewFiring = %d, want %d", len(ch.NewFiring), tc.wantNew)
			}
			if len(ch.Resolved) != tc.wantResolved {
				t.Errorf("Resolved = %d, want %d", len(ch.Resolved), tc.wantResolved)
			}
			if len(ch.Renotify) != tc.wantRenotify {
				t.Errorf("Renotify = %d, want %d", len(ch.Renotify), tc.wantRenotify)
			}
			if got := ch.Empty(); got != tc.wantEmpty {
				t.Errorf("Empty = %v, want %v", got, tc.wantEmpty)
			}
			if len(next.Active) != len(tc.alerts) {
				t.Errorf("next.Active = %d, want %d", len(next.Active), len(tc.alerts))
			}
		})
	}
}

func TestDiffPreservesFirstSeenAndSuppressesRenotifyStamp(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	alert := fixtureAlert("TailscaleNodeDown", "critical", "c5", "fp-1")
	firstSeen := now.Add(-30 * time.Hour)
	notified := now.Add(-2 * time.Hour)

	prev := NewState()
	prev.Active["fp-1"] = Entry{
		Fingerprint: "fp-1", AlertName: "TailscaleNodeDown", Severity: "critical",
		FirstSeenUnix: firstSeen.Unix(), LastNotifiedUnix: notified.Unix(),
	}

	_, next := Diff(prev, []Alert{alert}, now, 6*time.Hour)
	got := next.Active["fp-1"]
	if got.FirstSeenUnix != firstSeen.Unix() {
		t.Errorf("FirstSeenUnix = %d, want %d (must survive across runs)", got.FirstSeenUnix, firstSeen.Unix())
	}
	if got.LastNotifiedUnix != notified.Unix() {
		t.Errorf("LastNotifiedUnix = %d, want %d (unchanged when not renotified)", got.LastNotifiedUnix, notified.Unix())
	}

	// Once the window elapses the stamp advances so the next cycle waits again.
	ch2, next2 := Diff(prev, []Alert{alert}, now.Add(5*time.Hour), 6*time.Hour)
	if len(ch2.Renotify) != 1 {
		t.Fatalf("expected a renotify, got %d", len(ch2.Renotify))
	}
	if next2.Active["fp-1"].LastNotifiedUnix != now.Add(5*time.Hour).Unix() {
		t.Errorf("LastNotifiedUnix = %d, want the renotify time", next2.Active["fp-1"].LastNotifiedUnix)
	}
}

func TestDiffZeroLastNotifiedIsAlwaysDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	alert := fixtureAlert("A", "critical", "c5", "fp-z")
	prev := NewState()
	prev.Active["fp-z"] = Entry{Fingerprint: "fp-z", AlertName: "A", Severity: "critical"}

	ch, _ := Diff(prev, []Alert{alert}, now, 6*time.Hour)
	if len(ch.Renotify) != 1 {
		t.Fatalf("an entry with no notification stamp must never stay muted; got %d", len(ch.Renotify))
	}
}

func TestDiffOrderingIsDeterministic(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	alerts := []Alert{
		fixtureAlert("Zebra", "info", "c5", "z1"),
		fixtureAlert("Alpha", "warning", "c5", "a1"),
		fixtureAlert("Middle", "critical", "c5", "m1"),
		fixtureAlert("Alpha", "warning", "c7", "a2"),
	}
	want := []string{"Middle", "Alpha", "Alpha", "Zebra"}
	for i := 0; i < 20; i++ {
		ch, _ := Diff(NewState(), alerts, now, 0)
		for j, a := range ch.NewFiring {
			if a.Name() != want[j] {
				t.Fatalf("iteration %d: order = %v, want %v", i, names(ch.NewFiring), want)
			}
		}
	}
}

func names(alerts []Alert) []string {
	out := make([]string, 0, len(alerts))
	for i := range alerts {
		out = append(out, alerts[i].Name())
	}
	return out
}

func TestSeverityRank(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sev  string
		want int
	}{
		{"critical", 0}, {"page", 0}, {"warning", 1}, {"warn", 1},
		{"info", 2}, {"unknown", 3}, {"", 3}, {"debug", 3},
	}
	for _, tc := range tests {
		t.Run(tc.sev, func(t *testing.T) {
			t.Parallel()
			if got := severityRank(tc.sev); got != tc.want {
				t.Fatalf("severityRank(%q) = %d, want %d", tc.sev, got, tc.want)
			}
		})
	}
}
