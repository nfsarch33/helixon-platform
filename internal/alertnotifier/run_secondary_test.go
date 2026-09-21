// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package alertnotifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
)

// v18856: the digest now has three delivery tiers — the primary email, an
// always-on secondary (the ops Slack stream), and an urgent tier gated on
// critical severity (fleet-critical Slack + Telegram). These tests pin the
// contract the operator decision depends on: an urgent item that reaches
// ANY channel counts as delivered, so a vendor outage on one path can no
// longer black-hole the whole digest.

// recordingSender is a fakeSender that also counts concurrency-safe calls;
// reuse of fakeSender would be fine but these tests want per-call errors.
type errSequenceSender struct {
	mu   sync.Mutex
	errs []error
	sent []notify.Email
}

//nolint:gocritic // hugeParam: signature is fixed by the Sender interface
func (s *errSequenceSender) Send(_ context.Context, m notify.Email) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return err
	}
	return nil
}

func (s *errSequenceSender) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *errSequenceSender) callErrs(errs ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, errs...)
}

// warningOnlyAlerts is liveShapedAlerts without the critical alert, used to
// prove the urgent tier stays silent for non-critical changes.
const warningOnlyAlerts = `[
 {"labels":{"alertname":"FleetDoctorRed","severity":"warning","instance":"127.0.0.1:9100","job":"node-exporter"},
  "annotations":{"summary":"workspace-doctor verdict is RED on win1-wsl1"},
  "startsAt":"2026-08-27T14:12:18.908Z","fingerprint":"0778352592826b0a","status":{"state":"active"}}
]`

func secondaryTestConfig(t *testing.T, amBody string, primary, secondary, urgent Sender) (Config, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	am := newAMStub(t, amBody)
	state := filepath.Join(t.TempDir(), "state.json")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cfg := Config{
		AlertmanagerURL: am.srv.URL,
		StatePath:       state,
		TextfilePath:    filepath.Join(t.TempDir(), "textfile.prom"),
		Sender:          primary,
		SecondarySender: secondary,
		UrgentSender:    urgent,
		UrgentWhen:      HasCriticalChange,
		Stdout:          stdout,
		Stderr:          stderr,
	}
	return cfg, stdout, stderr
}

// TestSecondaryRescuesFailingPrimary pins the black-hole fix: email vendor
// down (the live Resend 403), secondary Slack up → the run succeeds, state
// advances, and the queued alert does not re-fire forever.
func TestSecondaryRescuesFailingPrimary(t *testing.T) {
	primary := &errSequenceSender{}
	primary.callErrs(errors.New("permanent failure: status 403"))
	secondary := &errSequenceSender{}

	cfg, _, _ := secondaryTestConfig(t, liveShapedAlerts, primary, secondary, nil)
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run err = %v, want nil when a secondary channel delivered", err)
	}
	if !res.Sent {
		t.Fatal("res.Sent = false, want true (secondary delivered)")
	}
	if !res.SecondarySent {
		t.Fatal("res.SecondarySent = false, want true")
	}
	if secondary.calls() != 1 {
		t.Fatalf("secondary calls = %d, want 1", secondary.calls())
	}
	if res.SendFailures != 1 {
		t.Fatalf("SendFailures = %d, want 1 (primary only)", res.SendFailures)
	}
	// State must advance: the whole point is releasing the queued backlog.
	raw, err := os.ReadFile(cfg.StatePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("state file not valid JSON: %.80s", raw)
	}
}

// TestUrgentSenderGatedOnCritical proves the urgent tier fires for a
// critical new alert and stays silent for warning-only changes.
func TestUrgentSenderGatedOnCritical(t *testing.T) {
	t.Run("critical fires urgent", func(t *testing.T) {
		urgent := &errSequenceSender{}
		cfg, _, _ := secondaryTestConfig(t, liveShapedAlerts, &errSequenceSender{}, nil, urgent)
		res, err := Run(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Run err = %v", err)
		}
		if urgent.calls() != 1 {
			t.Fatalf("urgent calls = %d, want 1", urgent.calls())
		}
		if !res.UrgentSent {
			t.Fatal("res.UrgentSent = false, want true")
		}
	})
	t.Run("warning-only stays silent", func(t *testing.T) {
		urgent := &errSequenceSender{}
		cfg, _, _ := secondaryTestConfig(t, warningOnlyAlerts, &errSequenceSender{}, nil, urgent)
		res, err := Run(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Run err = %v", err)
		}
		if urgent.calls() != 0 {
			t.Fatalf("urgent calls = %d, want 0", urgent.calls())
		}
		if res.UrgentSent {
			t.Fatal("res.UrgentSent = true, want false")
		}
	})
}

// TestAllChannelsFailsKeepsState pins the retry contract: when every
// configured channel fails, the state file must NOT advance so the next
// run redelivers the same report.
func TestAllChannelsFailKeepsState(t *testing.T) {
	primary := &errSequenceSender{}
	primary.callErrs(errors.New("primary down"))
	secondary := &errSequenceSender{}
	secondary.callErrs(errors.New("secondary down"))
	cfg, _, _ := secondaryTestConfig(t, liveShapedAlerts, primary, secondary, nil)
	res, err := Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run err = nil, want error when all channels fail")
	}
	if res.Sent {
		t.Fatal("res.Sent = true, want false")
	}
	if _, statErr := os.Stat(cfg.StatePath); !os.IsNotExist(statErr) {
		t.Fatal("state file written despite total delivery failure; queued report would be lost")
	}
}

// TestHasCriticalChange tables the severity predicate.
func TestHasCriticalChange(t *testing.T) {
	crit := Alert{Labels: map[string]string{"severity": "critical"}}
	warn := Alert{Labels: map[string]string{"severity": "warning"}}
	cases := []struct {
		name string
		ch   Change
		want bool
	}{
		{"empty", Change{}, false},
		{"new warning only", Change{NewFiring: []Alert{warn}}, false},
		{"new critical", Change{NewFiring: []Alert{warn, crit}}, true},
		{"renotify critical", Change{Renotify: []Alert{crit}}, true},
		{"resolved critical only", Change{Resolved: []Entry{{}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasCriticalChange(tc.ch); got != tc.want {
				t.Fatalf("HasCriticalChange = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAuditCarriesChannelOutcomes pins the NDJSON evidence: ops must be
// able to see WHICH tiers delivered from the audit line alone.
func TestAuditCarriesChannelOutcomes(t *testing.T) {
	cfg, stdout, _ := secondaryTestConfig(t, liveShapedAlerts, &errSequenceSender{}, &errSequenceSender{}, &errSequenceSender{})
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run err = %v", err)
	}
	var audit map[string]any
	var line string
	for _, l := range strings.Split(stdout.String(), "\n") {
		if strings.TrimSpace(l) != "" {
			line = l // keep the last non-empty line; one run emits one audit record
		}
	}
	if line == "" {
		t.Fatal("no audit line emitted")
	}
	if err := json.Unmarshal([]byte(line), &audit); err != nil {
		t.Fatalf("audit line not JSON: %v", err)
	}
	for _, key := range []string{"secondary_sent", "urgent_sent"} {
		if _, ok := audit[key]; !ok {
			t.Errorf("audit missing %q", key)
		}
	}
	if audit["urgent_sent"] != true {
		t.Errorf("audit urgent_sent = %v, want true (payload has a critical alert)", audit["urgent_sent"])
	}
}

// TestSecondaryIdleWithoutChange proves the always-on secondary does not
// fire when nothing changed — it mirrors the primary's quiet-fleet rule.
func TestSecondaryIdleWithoutChange(t *testing.T) {
	secondary := &errSequenceSender{}
	// Two runs over the same payload: the first delivers, the second sees
	// no change and must stay silent on every tier.
	cfg, _, _ := secondaryTestConfig(t, liveShapedAlerts, &errSequenceSender{}, secondary, nil)
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("first Run err = %v", err)
	}
	if secondary.calls() != 1 {
		t.Fatalf("after first run secondary calls = %d, want 1", secondary.calls())
	}
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("second Run err = %v", err)
	}
	if secondary.calls() != 1 {
		t.Fatalf("quiet second run spoke on secondary: calls = %d, want 1", secondary.calls())
	}
}

// silence unused-import style guards for future edits.
var _ = http.StatusOK
var _ = time.Now
