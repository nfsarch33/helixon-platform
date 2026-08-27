// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package alertnotifier

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixtureAlert builds a live-shaped Alertmanager v2 alert.
func fixtureAlert(name, sev, instance, fp string) Alert {
	return Alert{
		Labels: map[string]string{
			"alertname": name,
			"severity":  sev,
			"instance":  instance,
			"job":       "node-exporter",
		},
		Annotations: map[string]string{"summary": name + " on " + instance},
		StartsAt:    time.Date(2026, 8, 27, 14, 12, 18, 0, time.UTC),
		Fingerprint: fp,
		Status:      AlertStatus{State: "active"},
	}
}

func TestFingerprintOf(t *testing.T) {
	t.Parallel()
	base := fixtureAlert("TailscaleNodeDown", "critical", "c5", "")
	reordered := Alert{Labels: map[string]string{
		"job":       "node-exporter",
		"instance":  "c5",
		"severity":  "critical",
		"alertname": "TailscaleNodeDown",
	}}
	other := fixtureAlert("TailscaleNodeDown", "critical", "c7", "")

	tests := []struct {
		name  string
		alert Alert
		want  string
		equal Alert
		diff  Alert
	}{
		{name: "uses alertmanager fingerprint when present", alert: fixtureAlert("X", "info", "c5", "0778352592826b0a"), want: "0778352592826b0a"},
		{name: "trims whitespace fingerprint", alert: Alert{Fingerprint: "  abc123  "}, want: "abc123"},
		{name: "label order does not matter", alert: base, equal: reordered},
		{name: "different labels differ", alert: base, diff: other},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FingerprintOf(&tc.alert)
			if tc.want != "" && got != tc.want {
				t.Fatalf("FingerprintOf = %q, want %q", got, tc.want)
			}
			if tc.equal.Labels != nil && got != FingerprintOf(&tc.equal) {
				t.Fatalf("expected equal fingerprints, got %q vs %q", got, FingerprintOf(&tc.equal))
			}
			if tc.diff.Labels != nil && got == FingerprintOf(&tc.diff) {
				t.Fatalf("expected different fingerprints, both %q", got)
			}
			if got == "" {
				t.Fatal("fingerprint must never be empty")
			}
		})
	}
}

func TestAlertAccessors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		alert       Alert
		wantName    string
		wantSev     string
		wantLabels  []string
		wantNotify  bool
		description string
	}{
		{
			name:       "full labels",
			alert:      fixtureAlert("FleetDoctorRed", "Warning", "127.0.0.1:9100", "aa"),
			wantName:   "FleetDoctorRed",
			wantSev:    "warning",
			wantLabels: []string{"instance=127.0.0.1:9100", "job=node-exporter"},
			wantNotify: true,
		},
		{
			name:       "missing alertname and severity",
			alert:      Alert{Labels: map[string]string{"job": "x"}, Status: AlertStatus{State: "active"}},
			wantName:   "(unnamed)",
			wantSev:    severityUnknown,
			wantLabels: []string{"job=x"},
			wantNotify: true,
		},
		{
			name:       "suppressed alert is not notifiable",
			alert:      Alert{Labels: map[string]string{"alertname": "Silenced"}, Status: AlertStatus{State: "suppressed"}},
			wantName:   "Silenced",
			wantSev:    severityUnknown,
			wantLabels: []string{},
			wantNotify: false,
		},
		{
			name:       "resolved alert is not notifiable",
			alert:      Alert{Labels: map[string]string{"alertname": "Gone"}, Status: AlertStatus{State: "resolved"}},
			wantName:   "Gone",
			wantSev:    severityUnknown,
			wantLabels: []string{},
			wantNotify: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.alert.Name(); got != tc.wantName {
				t.Errorf("Name = %q, want %q", got, tc.wantName)
			}
			if got := tc.alert.Severity(); got != tc.wantSev {
				t.Errorf("Severity = %q, want %q", got, tc.wantSev)
			}
			if got := tc.alert.Notifiable(); got != tc.wantNotify {
				t.Errorf("Notifiable = %v, want %v", got, tc.wantNotify)
			}
			got := tc.alert.DistinguishingLabels()
			if len(got) != len(tc.wantLabels) {
				t.Fatalf("DistinguishingLabels = %v, want %v", got, tc.wantLabels)
			}
			for i := range got {
				if got[i] != tc.wantLabels[i] {
					t.Fatalf("DistinguishingLabels = %v, want %v", got, tc.wantLabels)
				}
			}
		})
	}
}

func TestNotifiableAlerts(t *testing.T) {
	t.Parallel()
	in := []Alert{
		fixtureAlert("A", "critical", "c5", "1"),
		{Labels: map[string]string{"alertname": "B"}, Status: AlertStatus{State: "suppressed"}},
		fixtureAlert("C", "warning", "c7", "3"),
	}
	got := NotifiableAlerts(in)
	if len(got) != 2 {
		t.Fatalf("NotifiableAlerts kept %d alerts, want 2: %+v", len(got), got)
	}
	if got[0].Name() != "A" || got[1].Name() != "C" {
		t.Fatalf("unexpected surviving alerts: %q %q", got[0].Name(), got[1].Name())
	}
}

func TestFetchAlerts(t *testing.T) {
	t.Parallel()
	const liveBody = `[{"labels":{"alertname":"FleetDoctorRed","severity":"warning","instance":"127.0.0.1:9100"},` +
		`"annotations":{"summary":"workspace-doctor verdict is RED"},` +
		`"startsAt":"2026-08-27T14:12:18.908Z","endsAt":"2026-08-27T16:03:48.908Z",` +
		`"fingerprint":"0778352592826b0a","status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}]`

	tests := []struct {
		name      string
		status    int
		body      string
		maxBody   int64
		wantCount int
		wantErr   string
	}{
		{name: "parses live-shaped payload", status: 200, body: liveBody, wantCount: 1},
		{name: "parses empty list", status: 200, body: `[]`, wantCount: 0},
		{name: "non-2xx is an error", status: 503, body: `service unavailable`, wantErr: "status 503"},
		{name: "malformed json is an error", status: 200, body: `{not json`, wantErr: "decode alertmanager response"},
		{name: "oversize body is refused not truncated", status: 200, body: liveBody, maxBody: 10, wantErr: "exceeds 10 bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != alertsPath {
					t.Errorf("polled %q, want %q", r.URL.Path, alertsPath)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := FetchAlerts(ctx, srv.Client(), srv.URL, tc.maxBody)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("FetchAlerts error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchAlerts: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("got %d alerts, want %d", len(got), tc.wantCount)
			}
			if tc.wantCount > 0 && got[0].Fingerprint != "0778352592826b0a" {
				t.Fatalf("fingerprint = %q", got[0].Fingerprint)
			}
		})
	}
}

// errDoer always fails, standing in for a down Alertmanager.
type errDoer struct{ err error }

func (d errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

func TestFetchAlertsTransportError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("connection refused")
	_, err := FetchAlerts(context.Background(), errDoer{err: sentinel}, DefaultAlertmanagerURL, 0)
	if err == nil || !strings.Contains(err.Error(), "poll alertmanager") {
		t.Fatalf("error = %v, want wrapped poll failure", err)
	}
}

func TestFetchAlertsRespectsContextDeadline(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := FetchAlerts(ctx, srv.Client(), srv.URL, 0); err == nil {
		t.Fatal("expected a deadline error, got nil")
	}
}
