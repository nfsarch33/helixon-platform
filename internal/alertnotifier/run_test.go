// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package alertnotifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
)

// liveShapedAlerts mirrors the production Alertmanager payload observed on
// 2026-08-28 (8 firing alerts across 5 rules), trimmed to three.
const liveShapedAlerts = `[
 {"labels":{"alertname":"FleetDoctorRed","severity":"warning","instance":"127.0.0.1:9100","job":"node-exporter"},
  "annotations":{"summary":"workspace-doctor verdict is RED on win1-wsl1"},
  "startsAt":"2026-08-27T14:12:18.908Z","fingerprint":"0778352592826b0a","status":{"state":"active"}},
 {"labels":{"alertname":"TailscaleNodeDown","severity":"critical","instance":"c5"},
  "annotations":{"summary":"tailnet node unreachable"},
  "startsAt":"2026-08-27T10:00:00Z","fingerprint":"43d404bfbb8ccf1c","status":{"state":"active"}},
 {"labels":{"alertname":"RouterFleetNodeUnhealthy","severity":"warning","instance":"c7"},
  "startsAt":"2026-08-27T11:00:00Z","fingerprint":"5897034939b701ca","status":{"state":"active"}}
]`

// fakeSender records what was handed to the vendor and can fail on demand.
type fakeSender struct {
	mu    sync.Mutex
	sent  []notify.Email
	err   error
	calls int
}

//nolint:gocritic // hugeParam: signature is fixed by the Sender interface
func (s *fakeSender) Send(_ context.Context, m notify.Email) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *fakeSender) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeSender) last() notify.Email {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return notify.Email{}
	}
	return s.sent[len(s.sent)-1]
}

// amStub is a mutable Alertmanager stand-in. Body and status are guarded
// because the test goroutine changes them between runs while the server
// goroutine reads them.
type amStub struct {
	mu     sync.Mutex
	body   string
	status int
	srv    *httptest.Server
}

func newAMStub(t *testing.T, body string) *amStub {
	t.Helper()
	s := &amStub{body: body, status: http.StatusOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != alertsPath {
			t.Errorf("polled %q, want %q", r.URL.Path, alertsPath)
		}
		s.mu.Lock()
		body, status := s.body, s.status
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *amStub) setBody(b string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = b
}

func (s *amStub) setStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}

func (s *amStub) url() string { return s.srv.URL }

// runHarness bundles the per-test temp paths and buffers. Every test MUST
// go through this so no run can ever touch the production state or
// textfile paths.
type runHarness struct {
	cfg    Config
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	dir    string
}

func newHarness(t *testing.T, amURL string, sender Sender, now time.Time) *runHarness {
	t.Helper()
	dir := t.TempDir()
	h := &runHarness{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, dir: dir}
	h.cfg = Config{
		AlertmanagerURL:  amURL,
		StatePath:        filepath.Join(dir, "state.json"),
		TextfilePath:     filepath.Join(dir, "alert-notifier.prom"),
		RenotifyInterval: 6 * time.Hour,
		HTTPTimeout:      5 * time.Second,
		HTTPClient:       &http.Client{Timeout: 5 * time.Second},
		Sender:           sender,
		Now:              func() time.Time { return now },
		Stdout:           h.stdout,
		Stderr:           h.stderr,
	}
	return h
}

// at pins the harness clock.
func (h *runHarness) at(ts time.Time) {
	h.cfg.Now = func() time.Time { return ts }
}

func (h *runHarness) metricsText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(h.cfg.TextfilePath) //nolint:gosec // G304: test-owned temp path
	if err != nil {
		t.Fatalf("textfile metrics missing: %v", err)
	}
	return string(raw)
}

func (h *runHarness) auditRecord(t *testing.T) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatalf("audit line is not NDJSON (%v): %q", err, h.stdout.String())
	}
	return rec
}

// gauge renders the expected exposition line for one gauge.
func gauge(name string, value int64) string {
	return fmt.Sprintf("hlxn_alert_notifier_%s %d", name, value)
}

// assertMetrics fails when any expected exposition line is absent.
func assertMetrics(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("metrics missing %q:\n%s", w, text)
		}
	}
}

func TestRunFirstRunNotifiesEverything(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	sender := &fakeSender{}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)

	res, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Sent || sender.count() != 1 {
		t.Fatalf("expected exactly one send, got sent=%v calls=%d", res.Sent, sender.count())
	}
	if len(res.Change.NewFiring) != 3 || len(res.Change.Resolved) != 0 {
		t.Fatalf("first run must report every firing alert as new: %+v", res.Change)
	}
	if res.ActiveAlerts != 3 {
		t.Fatalf("ActiveAlerts = %d, want 3", res.ActiveAlerts)
	}

	mail := sender.last()
	if mail.Subject != "[HELIXON] 3 new / 0 resolved" {
		t.Errorf("subject = %q", mail.Subject)
	}
	if len(mail.To) != 1 || mail.To[0] != DefaultTo {
		t.Errorf("To = %v, want the configured default", mail.To)
	}
	if mail.IdempotencyKey == "" {
		t.Error("every send must carry an idempotency key")
	}
	if !strings.Contains(mail.TextBody, "TailscaleNodeDown") {
		t.Errorf("body missing alertname:\n%s", mail.TextBody)
	}
	if !strings.Contains(mail.HTMLBody, "<pre") {
		t.Error("HTML alternative missing")
	}

	st, note := LoadState(h.cfg.StatePath)
	if note != "" {
		t.Fatalf("state note after first run: %q", note)
	}
	if len(st.Active) != 3 {
		t.Fatalf("state remembered %d alerts, want 3", len(st.Active))
	}
	if st.LastSuccessUnix != now.Unix() {
		t.Errorf("LastSuccessUnix = %d, want %d", st.LastSuccessUnix, now.Unix())
	}

	assertMetrics(t, h.metricsText(t),
		gauge("active_alerts", 3),
		gauge("send_failures_total", 0),
		gauge("last_run_timestamp_seconds", now.Unix()),
		gauge("last_success_timestamp_seconds", now.Unix()),
	)
	if rec := h.auditRecord(t); rec["sent"] != true {
		t.Errorf("audit record does not reflect the send: %v", rec)
	}
}

func TestRunNoChangeSendsNothing(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	sender := &fakeSender{}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)

	if _, err := Run(context.Background(), h.cfg); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	h.stdout.Reset()

	later := now.Add(10 * time.Minute)
	h.at(later)
	res, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.Sent || sender.count() != 1 {
		t.Fatalf("an unchanged fleet must send nothing; calls=%d", sender.count())
	}
	if !res.Change.Empty() {
		t.Fatalf("expected an empty change set, got %+v", res.Change)
	}

	assertMetrics(t, h.metricsText(t),
		// The heartbeat advances so a quiet fleet is distinguishable from
		// a dead notifier ...
		gauge("last_run_timestamp_seconds", later.Unix()),
		// ... but last_success is carried forward, not refreshed: nothing
		// reached the vendor on this run.
		gauge("last_success_timestamp_seconds", now.Unix()),
	)
	if rec := h.auditRecord(t); rec["sent"] != false {
		t.Errorf("audit record should show no send: %v", rec)
	}
}

func TestRunDetectsNewlyResolved(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	sender := &fakeSender{}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)

	if _, err := Run(context.Background(), h.cfg); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	am.setBody(`[
 {"labels":{"alertname":"TailscaleNodeDown","severity":"critical","instance":"c5"},
  "startsAt":"2026-08-27T10:00:00Z","fingerprint":"43d404bfbb8ccf1c","status":{"state":"active"}},
 {"labels":{"alertname":"RouterFleetNodeUnhealthy","severity":"warning","instance":"c7"},
  "startsAt":"2026-08-27T11:00:00Z","fingerprint":"5897034939b701ca","status":{"state":"active"}}
]`)
	h.at(now.Add(5 * time.Minute))
	res, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(res.Change.Resolved) != 1 || res.Change.Resolved[0].AlertName != "FleetDoctorRed" {
		t.Fatalf("expected FleetDoctorRed resolved, got %+v", res.Change.Resolved)
	}
	if len(res.Change.NewFiring) != 0 {
		t.Fatalf("nothing new should be reported, got %+v", res.Change.NewFiring)
	}
	if got := sender.last().Subject; got != "[HELIXON] 0 new / 1 resolved" {
		t.Fatalf("subject = %q", got)
	}
	if !strings.Contains(sender.last().TextBody, "RESOLVED (1)") {
		t.Errorf("resolved section missing:\n%s", sender.last().TextBody)
	}
	st, _ := LoadState(h.cfg.StatePath)
	if len(st.Active) != 2 {
		t.Fatalf("resolved alert must be dropped from state, still have %d", len(st.Active))
	}
}

func TestRunRenotifySuppressionAndRelease(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, `[{"labels":{"alertname":"TailscaleNodeDown","severity":"critical","instance":"c5"},
 "startsAt":"2026-08-27T10:00:00Z","fingerprint":"43d404bfbb8ccf1c","status":{"state":"active"}}]`)
	sender := &fakeSender{}
	start := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, start)

	if _, err := Run(context.Background(), h.cfg); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("first run should send once, got %d", sender.count())
	}

	// Inside the renotify window: silent.
	for _, offset := range []time.Duration{5 * time.Minute, time.Hour, 5*time.Hour + 59*time.Minute} {
		h.at(start.Add(offset))
		if _, err := Run(context.Background(), h.cfg); err != nil {
			t.Fatalf("Run at +%v: %v", offset, err)
		}
		if sender.count() != 1 {
			t.Fatalf("renotify fired early at +%v (calls=%d)", offset, sender.count())
		}
	}

	// Past the window: exactly one repeat.
	h.at(start.Add(6*time.Hour + time.Minute))
	res, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("Run past window: %v", err)
	}
	if sender.count() != 2 {
		t.Fatalf("expected a renotify send, calls=%d", sender.count())
	}
	if len(res.Change.Renotify) != 1 {
		t.Fatalf("expected 1 renotify, got %+v", res.Change)
	}
	if got := sender.last().Subject; !strings.Contains(got, "1 still firing") {
		t.Fatalf("subject = %q, want a still-firing count", got)
	}

	// And the window restarts.
	h.at(start.Add(6*time.Hour + 2*time.Minute))
	if _, err := Run(context.Background(), h.cfg); err != nil {
		t.Fatalf("Run after renotify: %v", err)
	}
	if sender.count() != 2 {
		t.Fatalf("renotify window did not restart, calls=%d", sender.count())
	}
}

func TestRunSendFailureKeepsStateForRetry(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	sender := &fakeSender{err: errors.New("vendor exploded")}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)

	res, err := Run(context.Background(), h.cfg)
	if err == nil {
		t.Fatal("a failed send must be reported as an error")
	}
	if res.Sent {
		t.Error("Sent must be false when the vendor rejected the mail")
	}
	if res.SendFailures != 1 {
		t.Errorf("SendFailures = %d, want 1", res.SendFailures)
	}

	// State must NOT advance, so the next run retries the same digest.
	if _, statErr := os.Stat(h.cfg.StatePath); !os.IsNotExist(statErr) {
		st, _ := LoadState(h.cfg.StatePath)
		if len(st.Active) != 0 {
			t.Fatalf("state advanced despite a failed send: %+v", st)
		}
	}

	assertMetrics(t, h.metricsText(t),
		gauge("send_failures_total", 1),
		// last_success must not advance ...
		gauge("last_success_timestamp_seconds", 0),
		// ... but the run itself did complete, so the heartbeat does.
		gauge("last_run_timestamp_seconds", now.Unix()),
	)

	// The retry on the next run must resend the identical digest.
	sender.setErr(nil)
	h.at(now.Add(5 * time.Minute))
	res2, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("retry Run: %v", err)
	}
	if len(res2.Change.NewFiring) != 3 {
		t.Fatalf("retry lost the undelivered alerts: %+v", res2.Change)
	}
	if res.Digest.IdempotencyKey != res2.Digest.IdempotencyKey {
		t.Errorf("retry must reuse the idempotency key: %q vs %q",
			res.Digest.IdempotencyKey, res2.Digest.IdempotencyKey)
	}
}

func TestRunAlertmanagerFailureCarriesMetricsForward(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	sender := &fakeSender{}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)

	if _, err := Run(context.Background(), h.cfg); err != nil {
		t.Fatalf("seed Run: %v", err)
	}

	am.setStatus(http.StatusInternalServerError)
	h.at(now.Add(time.Hour))
	_, err := Run(context.Background(), h.cfg)
	if err == nil {
		t.Fatal("an unreachable Alertmanager must be an error")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("error = %v", err)
	}

	assertMetrics(t, h.metricsText(t),
		// last_run must NOT advance: the run did not complete, so a
		// staleness rule on this gauge is what surfaces the outage.
		gauge("last_run_timestamp_seconds", now.Unix()),
		gauge("last_success_timestamp_seconds", now.Unix()),
		gauge("active_alerts", 3),
	)
	if sender.count() != 1 {
		t.Errorf("no send should be attempted when the poll failed, calls=%d", sender.count())
	}
}

func TestRunDryRunHasNoSideEffects(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	sender := &fakeSender{}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)
	h.cfg.DryRun = true

	res, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Sent || sender.count() != 0 {
		t.Fatalf("dry-run must not send, calls=%d", sender.count())
	}
	if _, err := os.Stat(h.cfg.StatePath); !os.IsNotExist(err) {
		t.Error("dry-run must not write the state file")
	}
	if _, err := os.Stat(h.cfg.TextfilePath); !os.IsNotExist(err) {
		t.Error("dry-run must not perturb the production textfile metrics")
	}
	rec := h.auditRecord(t)
	if rec["dry_run"] != true {
		t.Errorf("audit record must be marked dry-run: %v", rec)
	}
	if rec["new_firing"] != float64(3) {
		t.Errorf("audit record new_firing = %v, want 3", rec["new_firing"])
	}
	if !strings.Contains(h.stderr.String(), "rendered digest") ||
		!strings.Contains(h.stderr.String(), "TailscaleNodeDown") {
		t.Errorf("dry-run must render the email for inspection:\n%s", h.stderr.String())
	}
}

func TestRunCorruptStateIsTreatedAsFirstRun(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	sender := &fakeSender{}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)
	if err := os.WriteFile(h.cfg.StatePath, []byte("\x00\x01 not json at all"), 0o600); err != nil {
		t.Fatalf("seed corrupt state: %v", err)
	}

	res, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("a corrupt state file must not crash the alerter: %v", err)
	}
	if len(res.Change.NewFiring) != 3 {
		t.Fatalf("expected a first-run digest, got %+v", res.Change)
	}
	if !strings.Contains(res.StateNote, "corrupt") {
		t.Errorf("StateNote = %q, want a corruption note", res.StateNote)
	}
	if rec := h.auditRecord(t); rec["state_note"] == nil {
		t.Error("the corruption must be visible in the audit record")
	}
	// And the damaged file must have been replaced by a valid one.
	if _, note := LoadState(h.cfg.StatePath); note != "" {
		t.Errorf("state was not repaired: %q", note)
	}
}

func TestRunWithoutSenderIsAnError(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, liveShapedAlerts)
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), nil, now)
	h.cfg.Sender = nil

	_, err := Run(context.Background(), h.cfg)
	if err == nil || !strings.Contains(err.Error(), "no sender configured") {
		t.Fatalf("error = %v, want a missing-sender failure", err)
	}
	assertMetrics(t, h.metricsText(t), gauge("send_failures_total", 1))
}

func TestRunSuppressedAlertsDoNotNotify(t *testing.T) {
	t.Parallel()
	am := newAMStub(t, `[{"labels":{"alertname":"Silenced","severity":"critical"},"fingerprint":"s1",
 "startsAt":"2026-08-27T10:00:00Z","status":{"state":"suppressed","silencedBy":["abc"]}}]`)
	sender := &fakeSender{}
	now := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	h := newHarness(t, am.url(), sender, now)

	res, err := Run(context.Background(), h.cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("a silenced alert must not reach a human, calls=%d", sender.count())
	}
	if res.ActiveAlerts != 0 {
		t.Fatalf("ActiveAlerts = %d, want 0", res.ActiveAlerts)
	}
}

func TestConfigDefaults(t *testing.T) {
	t.Parallel()
	got := Config{}.withDefaults()
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"alertmanager url", got.AlertmanagerURL, DefaultAlertmanagerURL},
		{"state path", got.StatePath, DefaultStatePath},
		{"textfile path", got.TextfilePath, DefaultTextfilePath},
		{"from", got.From, DefaultFrom},
		{"job id", got.JobID, DefaultJobID},
		{"renotify", got.RenotifyInterval, DefaultRenotifyInterval},
		{"timeout", got.HTTPTimeout, DefaultHTTPTimeout},
		{"max body", got.MaxBodyBytes, DefaultMaxBodyBytes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		})
	}
	if len(got.To) != 1 || got.To[0] != DefaultTo {
		t.Fatalf("To = %v, want %v", got.To, []string{DefaultTo})
	}
	if got.HTTPClient == nil || got.Now == nil || got.Stdout == nil || got.Stderr == nil {
		t.Fatal("withDefaults left a required collaborator nil")
	}
}
