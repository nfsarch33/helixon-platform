package alertnotifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderMetricsExposition(t *testing.T) {
	t.Parallel()
	got := RenderMetrics(Metrics{
		LastRunUnix:     1756000000,
		LastSuccessUnix: 1755999000,
		ActiveAlerts:    8,
		SendFailures:    1,
	})

	// The HELP strings are a contract: node_exporter refuses to merge two
	// .prom files whose HELP for a metric disagrees, so assert them
	// byte-for-byte rather than by substring of a substring.
	wantLines := []string{
		"# HELP hlxn_alert_notifier_last_run_timestamp_seconds Unix time of the last completed alert-notifier run.",
		"# TYPE hlxn_alert_notifier_last_run_timestamp_seconds gauge",
		"hlxn_alert_notifier_last_run_timestamp_seconds 1756000000",
		"# HELP hlxn_alert_notifier_last_success_timestamp_seconds Unix time of the last run that reached the notification vendor successfully.",
		"# TYPE hlxn_alert_notifier_last_success_timestamp_seconds gauge",
		"hlxn_alert_notifier_last_success_timestamp_seconds 1755999000",
		"# HELP hlxn_alert_notifier_active_alerts Number of alerts currently active in Alertmanager as seen by the notifier.",
		"# TYPE hlxn_alert_notifier_active_alerts gauge",
		"hlxn_alert_notifier_active_alerts 8",
		"# HELP hlxn_alert_notifier_send_failures_total Count of notification send failures observed by the last run.",
		"# TYPE hlxn_alert_notifier_send_failures_total gauge",
		"hlxn_alert_notifier_send_failures_total 1",
	}
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != len(wantLines) {
		t.Fatalf("exposition has %d lines, want %d:\n%s", len(lines), len(wantLines), got)
	}
	for i, want := range wantLines {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("exposition must end with a newline")
	}
}

func TestRenderMetricsZeroValues(t *testing.T) {
	t.Parallel()
	got := RenderMetrics(Metrics{})
	for _, want := range []string{
		"hlxn_alert_notifier_last_run_timestamp_seconds 0",
		"hlxn_alert_notifier_last_success_timestamp_seconds 0",
		"hlxn_alert_notifier_active_alerts 0",
		"hlxn_alert_notifier_send_failures_total 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWriteMetricsAtomicAndReadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "alert-notifier.prom")

	if err := WriteMetrics(path, Metrics{LastRunUnix: 42, ActiveAlerts: 8}); err != nil {
		t.Fatalf("WriteMetrics: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644 (the textfile collector must be able to read it)", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: test-owned temp path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "hlxn_alert_notifier_active_alerts 8") {
		t.Fatalf("unexpected content:\n%s", raw)
	}
	assertNoTempFiles(t, filepath.Dir(path), "alert-notifier.prom")

	// Rewriting must replace, not append, and must still be clean.
	if err := WriteMetrics(path, Metrics{LastRunUnix: 43, ActiveAlerts: 0}); err != nil {
		t.Fatalf("WriteMetrics rewrite: %v", err)
	}
	raw, err = os.ReadFile(path) //nolint:gosec // G304: test-owned temp path
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if strings.Contains(string(raw), "active_alerts 8") {
		t.Fatalf("rewrite appended instead of replacing:\n%s", raw)
	}
	if strings.Count(string(raw), "# TYPE hlxn_alert_notifier_active_alerts gauge") != 1 {
		t.Fatalf("duplicate TYPE lines would break node_exporter merge:\n%s", raw)
	}
	assertNoTempFiles(t, filepath.Dir(path), "alert-notifier.prom")
}
