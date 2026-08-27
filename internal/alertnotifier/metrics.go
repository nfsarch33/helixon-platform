package alertnotifier

import (
	"fmt"
	"os"
	"strings"
)

// DefaultTextfilePath is the node-exporter textfile collector drop the
// notifier exports itself through. This estate has been burned by streams
// that died silently; the alerter must be alertable.
const DefaultTextfilePath = "/home/jaslian/.local/share/node-exporter-textfile/alert-notifier.prom"

// TextfileMode is 0644 because the node-exporter textfile collector reads
// the file and may not run as the writing user.
const TextfileMode os.FileMode = 0o644

// Metrics is the exported self-observability of one run.
//
// Semantics, chosen so no combination of them can look healthy while the
// notifier is dead:
//
//   - LastRunUnix advances only when the run obtained a valid alert list.
//     A run that could not reach Alertmanager carries the previous value
//     forward, so the file goes stale and a staleness rule fires.
//   - LastSuccessUnix advances only when the notification vendor accepted
//     a send. It is carried forward on runs with nothing to send.
//   - ActiveAlerts is what this notifier saw, which is the number that
//     matters when reconciling against Alertmanager's own metrics.
//   - SendFailures is the failure count of the LAST run (a gauge, not a
//     monotonic counter), so a single failing run is visible immediately
//     without waiting for a rate() window.
type Metrics struct {
	LastRunUnix     int64
	LastSuccessUnix int64
	ActiveAlerts    int
	SendFailures    int
}

// metricLine is one gauge's help text, name, and value.
type metricLine struct {
	name  string
	help  string
	value string
}

// RenderMetrics returns the Prometheus text-format exposition. The HELP
// strings are byte-for-byte fixed: node_exporter refuses to merge two
// .prom files that disagree on a metric's HELP, so these must never drift.
func RenderMetrics(m Metrics) string {
	lines := []metricLine{
		{
			name:  "hlxn_alert_notifier_last_run_timestamp_seconds",
			help:  "Unix time of the last completed alert-notifier run.",
			value: fmt.Sprintf("%d", m.LastRunUnix),
		},
		{
			name:  "hlxn_alert_notifier_last_success_timestamp_seconds",
			help:  "Unix time of the last run that reached the notification vendor successfully.",
			value: fmt.Sprintf("%d", m.LastSuccessUnix),
		},
		{
			name:  "hlxn_alert_notifier_active_alerts",
			help:  "Number of alerts currently active in Alertmanager as seen by the notifier.",
			value: fmt.Sprintf("%d", m.ActiveAlerts),
		},
		{
			name:  "hlxn_alert_notifier_send_failures_total",
			help:  "Count of notification send failures observed by the last run.",
			value: fmt.Sprintf("%d", m.SendFailures),
		},
	}
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", l.name, l.help, l.name, l.name, l.value)
	}
	return b.String()
}

// WriteMetrics atomically writes the textfile export at mode 0644.
func WriteMetrics(path string, m Metrics) error {
	if path == "" {
		path = DefaultTextfilePath
	}
	if err := WriteFileAtomic(path, []byte(RenderMetrics(m)), TextfileMode); err != nil {
		return fmt.Errorf("write textfile metrics: %w", err)
	}
	return nil
}
