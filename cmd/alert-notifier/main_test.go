// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/alertnotifier"
)

// envMap turns a map into the getenv function run() expects, so no test
// ever mutates the real process environment.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseArgsDefaults(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	opts, code := parseArgs(nil, &stderr, envMap(nil))
	if code != exitOK {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"alertmanager url", opts.cfg.AlertmanagerURL, alertnotifier.DefaultAlertmanagerURL},
		{"state path", opts.cfg.StatePath, alertnotifier.DefaultStatePath},
		{"textfile path", opts.cfg.TextfilePath, alertnotifier.DefaultTextfilePath},
		{"from", opts.cfg.From, alertnotifier.DefaultFrom},
		{"job id", opts.cfg.JobID, alertnotifier.DefaultJobID},
		{"renotify", opts.cfg.RenotifyInterval, alertnotifier.DefaultRenotifyInterval},
		{"timeout", opts.cfg.HTTPTimeout, alertnotifier.DefaultHTTPTimeout},
		{"dry run", opts.cfg.DryRun, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		})
	}
	if len(opts.cfg.To) != 1 || opts.cfg.To[0] != alertnotifier.DefaultTo {
		t.Fatalf("To = %v, want the free-tier account owner", opts.cfg.To)
	}
}

func TestParseArgsFlagsAndEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		check    func(t *testing.T, o options)
	}{
		{
			name: "env supplies every knob",
			env: map[string]string{
				"HLXN_ALERTMANAGER_URL":        "http://127.0.0.1:19093",
				"HLXN_ALERT_NOTIFIER_STATE":    "/tmp/s.json",
				"HLXN_ALERT_NOTIFIER_TEXTFILE": "/tmp/m.prom",
				"HLXN_ALERT_NOTIFIER_FROM":     "alerts@example.test",
				"HLXN_ALERT_NOTIFIER_TO":       "a@example.test, b@example.test",
				"HLXN_ALERT_NOTIFIER_RENOTIFY": "90m",
				"HLXN_ALERT_NOTIFIER_TIMEOUT":  "3s",
				"HLXN_ALERT_NOTIFIER_JOB_ID":   "job-9",
				"RESEND_API_KEY":               "re_env_only_never_argv",
			},
			check: func(t *testing.T, o options) {
				t.Helper()
				if o.cfg.AlertmanagerURL != "http://127.0.0.1:19093" || o.cfg.StatePath != "/tmp/s.json" {
					t.Errorf("env not applied: %+v", o.cfg)
				}
				if o.cfg.From != "alerts@example.test" || o.cfg.JobID != "job-9" {
					t.Errorf("env not applied: %+v", o.cfg)
				}
				if len(o.cfg.To) != 2 || o.cfg.To[1] != "b@example.test" {
					t.Errorf("recipient list = %v", o.cfg.To)
				}
				if o.cfg.RenotifyInterval != 90*time.Minute || o.cfg.HTTPTimeout != 3*time.Second {
					t.Errorf("durations = %v / %v", o.cfg.RenotifyInterval, o.cfg.HTTPTimeout)
				}
				if o.apiKey != "re_env_only_never_argv" {
					t.Error("API key must be read from the environment")
				}
			},
		},
		{
			name: "flags beat env",
			args: []string{"--from", "flag@example.test", "--renotify", "15m", "--dry-run"},
			env: map[string]string{
				"HLXN_ALERT_NOTIFIER_FROM":     "env@example.test",
				"HLXN_ALERT_NOTIFIER_RENOTIFY": "90m",
			},
			check: func(t *testing.T, o options) {
				t.Helper()
				if o.cfg.From != "flag@example.test" {
					t.Errorf("From = %q", o.cfg.From)
				}
				if o.cfg.RenotifyInterval != 15*time.Minute {
					t.Errorf("renotify = %v", o.cfg.RenotifyInterval)
				}
				if !o.cfg.DryRun {
					t.Error("--dry-run not honored")
				}
			},
		},
		{
			name:     "unknown flag is a usage error",
			args:     []string{"--nope"},
			wantCode: exitUsage,
		},
		{
			name:     "positional argument is a usage error",
			args:     []string{"stray"},
			wantCode: exitUsage,
		},
		{
			name:     "unparseable env duration is a usage error",
			env:      map[string]string{"HLXN_ALERT_NOTIFIER_RENOTIFY": "six hours"},
			wantCode: exitUsage,
		},
		{
			name:     "non-positive env duration is a usage error",
			env:      map[string]string{"HLXN_ALERT_NOTIFIER_TIMEOUT": "0s"},
			wantCode: exitUsage,
		},
		{
			name:     "empty recipient list is a usage error",
			env:      map[string]string{"HLXN_ALERT_NOTIFIER_TO": " , ,"},
			wantCode: exitUsage,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			opts, code := parseArgs(tc.args, &stderr, envMap(tc.env))
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stderr: %s)", code, tc.wantCode, stderr.String())
			}
			if tc.check != nil {
				tc.check(t, opts)
			}
		})
	}
}

func TestBuildSenderRefusesWithoutAKey(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	_, code := buildSender(&options{cfg: alertnotifier.Config{}}, &stderr)
	if code != exitUsage {
		t.Fatalf("code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "RESEND_API_KEY") {
		t.Errorf("the operator must be told which variable is missing: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--dry-run") {
		t.Errorf("the message should point at the safe inspection path: %s", stderr.String())
	}
}

func TestBuildSenderDryRunNeedsNoKey(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	sender, code := buildSender(&options{cfg: alertnotifier.Config{DryRun: true}}, &stderr)
	if code != exitOK {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if sender != nil {
		t.Error("dry-run must not construct a live vendor client")
	}
}

func TestBuildSenderReturnsCanonicalClient(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	sender, code := buildSender(&options{
		cfg:    alertnotifier.Config{From: alertnotifier.DefaultFrom, HTTPTimeout: time.Second},
		apiKey: "re_NOTAREAL_buildsender0000",
	}, &stderr)
	if code != exitOK {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if sender == nil {
		t.Fatal("expected a sender")
	}
}

func TestRunDryRunEndToEnd(t *testing.T) {
	t.Parallel()
	const body = `[{"labels":{"alertname":"TailscaleNodeDown","severity":"critical","instance":"c5"},
 "annotations":{"summary":"tailnet node unreachable"},
 "startsAt":"2026-08-27T10:00:00Z","fingerprint":"43d404bfbb8ccf1c","status":{"state":"active"}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	textPath := filepath.Join(dir, "alert-notifier.prom")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--alertmanager-url", srv.URL,
		"--state", statePath,
		"--textfile", textPath,
		"--dry-run",
	}, &stdout, &stderr, envMap(nil))
	if code != exitOK {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &rec); err != nil {
		t.Fatalf("stdout is not a single NDJSON record (%v): %q", err, stdout.String())
	}
	if rec["dry_run"] != true || rec["new_firing"] != float64(1) {
		t.Fatalf("unexpected audit record: %v", rec)
	}
	if !strings.Contains(stderr.String(), "TailscaleNodeDown") {
		t.Errorf("the digest should be rendered for the operator: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "alert-notifier: active=1") {
		t.Errorf("missing run summary: %s", stderr.String())
	}
	for _, p := range []string{statePath, textPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("dry-run wrote %s", p)
		}
	}
}

func TestRunReportsAlertmanagerFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--alertmanager-url", srv.URL,
		"--state", filepath.Join(dir, "state.json"),
		"--textfile", filepath.Join(dir, "m.prom"),
		"--dry-run",
	}, &stdout, &stderr, envMap(nil))
	if code != exitFailure {
		t.Fatalf("code = %d, want %d (stderr: %s)", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), "status 502") {
		t.Errorf("stderr should name the failure: %s", stderr.String())
	}
}

func TestRunUsageErrorExitsTwo(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--not-a-flag"}, &stdout, &stderr, envMap(nil)); code != exitUsage {
		t.Fatalf("code = %d, want %d", code, exitUsage)
	}
}

func TestRunWithoutKeyExitsUsage(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"--alertmanager-url", "http://127.0.0.1:9093"}, &stdout, &stderr, envMap(nil))
	if code != exitUsage {
		t.Fatalf("code = %d, want %d (a notifier with no delivery path must fail loudly)", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("no audit record should be emitted for a configuration error: %q", stdout.String())
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	t.Run("firstNonEmpty", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			in   []string
			want string
		}{
			{[]string{"", "  ", "x"}, "x"},
			{[]string{" trimmed ", "y"}, "trimmed"},
			{[]string{"", ""}, ""},
			{nil, ""},
		}
		for _, tc := range tests {
			if got := firstNonEmpty(tc.in...); got != tc.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})
	t.Run("splitRecipients", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			in   string
			want []string
		}{
			{"a@x.test", []string{"a@x.test"}},
			{" a@x.test , b@x.test ", []string{"a@x.test", "b@x.test"}},
			{",,", nil},
			{"", nil},
		}
		for _, tc := range tests {
			got := splitRecipients(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitRecipients(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitRecipients(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		}
	})
	t.Run("overallTimeout", func(t *testing.T) {
		t.Parallel()
		if got := overallTimeout(alertnotifier.Config{}); got != 6*alertnotifier.DefaultHTTPTimeout {
			t.Errorf("overallTimeout = %v", got)
		}
		if got := overallTimeout(alertnotifier.Config{HTTPTimeout: time.Second}); got != 6*time.Second {
			t.Errorf("overallTimeout = %v", got)
		}
	})
}
