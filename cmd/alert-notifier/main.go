// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
//
// Command alert-notifier polls Alertmanager and emails a digest of what
// changed, so fleet alerts reach a human.
//
// It exists because every alert this estate has ever built fired into a
// black hole: the Slack incoming webhook Alertmanager was configured with
// is revoked (it 302s to the Slack docs page), and the remaining receivers
// only POST to a local recorder with no notification code.
//
// Design notes:
//
//   - This is a POLLER, not a webhook receiver. It opens no listening
//     port, needs no Alertmanager configuration change (so the live
//     alerting stack is never restarted to install it), and is
//     crash-safe/resumable because all cross-run memory is one atomically
//     written state file.
//   - Delivery is HTTP-API-only through internal/notify (ADR-0087 forbids
//     SMTP; ADR-077 makes internal/notify the canonical sink).
//   - The API key is read from the RESEND_API_KEY environment variable
//     only. It is never accepted as a command-line flag, because argv is
//     world-readable via /proc.
//
// Usage:
//
//	alert-notifier [--alertmanager-url URL] [--state PATH] [--textfile PATH]
//	               [--from ADDR] [--to ADDR[,ADDR]] [--renotify 6h]
//	               [--timeout 10s] [--job-id ID] [--dry-run]
//
// Exit codes: 0 success (including "nothing changed"), 1 runtime failure
// (Alertmanager unreachable, send rejected), 2 usage or configuration
// error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/alertnotifier"
	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

// apiKeyEnv is the only channel by which the vendor credential enters this
// process. The systemd unit renders it into an EnvironmentFile.
const apiKeyEnv = "RESEND_API_KEY" //nolint:gosec // G101: an env var NAME, not a credential

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// options is the parsed CLI surface.
type options struct {
	cfg    alertnotifier.Config
	apiKey string
}

// run is the testable entry point: it returns an exit code instead of
// calling os.Exit, and takes its environment as a function so tests never
// mutate the process environment.
func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	opts, code := parseArgs(args, stderr, getenv)
	if code != exitOK {
		return code
	}
	opts.cfg.Stdout = stdout
	opts.cfg.Stderr = stderr

	sender, code := buildSender(&opts, stderr)
	if code != exitOK {
		return code
	}
	opts.cfg.Sender = sender

	// Bound the whole cycle, not just the individual calls: a wedged
	// vendor socket must never leave a timer-driven process resident.
	ctx, cancel := context.WithTimeout(context.Background(), overallTimeout(opts.cfg))
	defer cancel()

	res, err := alertnotifier.Run(ctx, opts.cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return exitFailure
	}
	summarize(stderr, &res)
	return exitOK
}

// overallTimeout bounds one full cycle: one Alertmanager poll plus the
// vendor send with its bounded retry budget.
//
//nolint:gocritic // hugeParam: reads two fields off a value the caller already holds
func overallTimeout(cfg alertnotifier.Config) time.Duration {
	t := cfg.HTTPTimeout
	if t <= 0 {
		t = alertnotifier.DefaultHTTPTimeout
	}
	return 6 * t
}

// summarize writes a one-line human summary to stderr; stdout stays a
// clean NDJSON audit stream.
func summarize(stderr io.Writer, res *alertnotifier.Result) {
	_, _ = fmt.Fprintf(stderr, "alert-notifier: active=%d new=%d resolved=%d renotify=%d sent=%v dry_run=%v\n",
		res.ActiveAlerts, len(res.Change.NewFiring), len(res.Change.Resolved),
		len(res.Change.Renotify), res.Sent, res.DryRun)
}

// parseArgs builds the run configuration from flags with environment
// fallbacks. Every knob has an env twin so a systemd unit can configure
// the poller without editing its ExecStart line.
func parseArgs(args []string, stderr io.Writer, getenv func(string) string) (options, int) {
	fs := flag.NewFlagSet("alert-notifier", flag.ContinueOnError)
	fs.SetOutput(stderr)

	amURL := fs.String("alertmanager-url", "", "Alertmanager base URL (env HLXN_ALERTMANAGER_URL)")
	statePath := fs.String("state", "", "path to the seen-alert state file (env HLXN_ALERT_NOTIFIER_STATE)")
	textfile := fs.String("textfile", "", "path to the node-exporter textfile export (env HLXN_ALERT_NOTIFIER_TEXTFILE)")
	from := fs.String("from", "", "sender address (env HLXN_ALERT_NOTIFIER_FROM)")
	to := fs.String("to", "", "comma-separated recipients (env HLXN_ALERT_NOTIFIER_TO)")
	jobID := fs.String("job-id", "", "cost-attribution job id (env HLXN_ALERT_NOTIFIER_JOB_ID)")
	renotify := fs.Duration("renotify", 0, "quiet period before repeating a still-firing alert (env HLXN_ALERT_NOTIFIER_RENOTIFY)")
	timeout := fs.Duration("timeout", 0, "per-HTTP-call timeout (env HLXN_ALERT_NOTIFIER_TIMEOUT)")
	dryRun := fs.Bool("dry-run", false, "render the digest and emit the audit record without sending")

	if err := fs.Parse(args); err != nil {
		return options{}, exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "ERROR: unexpected argument %q\n", fs.Arg(0))
		return options{}, exitUsage
	}

	cfg := alertnotifier.Config{
		AlertmanagerURL: firstNonEmpty(*amURL, getenv("HLXN_ALERTMANAGER_URL"), alertnotifier.DefaultAlertmanagerURL),
		StatePath:       firstNonEmpty(*statePath, getenv("HLXN_ALERT_NOTIFIER_STATE"), alertnotifier.DefaultStatePath),
		TextfilePath:    firstNonEmpty(*textfile, getenv("HLXN_ALERT_NOTIFIER_TEXTFILE"), alertnotifier.DefaultTextfilePath),
		From:            firstNonEmpty(*from, getenv("HLXN_ALERT_NOTIFIER_FROM"), alertnotifier.DefaultFrom),
		To:              splitRecipients(firstNonEmpty(*to, getenv("HLXN_ALERT_NOTIFIER_TO"), alertnotifier.DefaultTo)),
		JobID:           firstNonEmpty(*jobID, getenv("HLXN_ALERT_NOTIFIER_JOB_ID"), alertnotifier.DefaultJobID),
		DryRun:          *dryRun,
	}

	var code int
	if cfg.RenotifyInterval, code = resolveDuration(*renotify, getenv("HLXN_ALERT_NOTIFIER_RENOTIFY"),
		alertnotifier.DefaultRenotifyInterval, "renotify", stderr); code != exitOK {
		return options{}, code
	}
	if cfg.HTTPTimeout, code = resolveDuration(*timeout, getenv("HLXN_ALERT_NOTIFIER_TIMEOUT"),
		alertnotifier.DefaultHTTPTimeout, "timeout", stderr); code != exitOK {
		return options{}, code
	}
	if len(cfg.To) == 0 {
		_, _ = fmt.Fprintln(stderr, "ERROR: --to resolved to an empty recipient list")
		return options{}, exitUsage
	}

	return options{cfg: cfg, apiKey: getenv(apiKeyEnv)}, exitOK
}

// resolveDuration picks the flag, then the env var, then the default,
// rejecting an unparseable or non-positive env value rather than silently
// substituting a default that changes the notifier's behavior.
func resolveDuration(flagVal time.Duration, envVal string, def time.Duration, name string, stderr io.Writer) (time.Duration, int) {
	if flagVal > 0 {
		return flagVal, exitOK
	}
	if envVal == "" {
		return def, exitOK
	}
	parsed, err := time.ParseDuration(envVal)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ERROR: invalid %s duration %q: %v\n", name, envVal, err)
		return 0, exitUsage
	}
	if parsed <= 0 {
		_, _ = fmt.Fprintf(stderr, "ERROR: %s duration must be positive, got %q\n", name, envVal)
		return 0, exitUsage
	}
	return parsed, exitOK
}

// buildSender constructs the canonical Resend client, reusing the
// package's idempotency store, retry classification, body sanitisation,
// and metrics registry.
//
// A missing key is a hard failure, not a silent downgrade to dry-run: a
// notifier that quietly stops notifying is the exact defect this command
// was written to end.
func buildSender(opts *options, stderr io.Writer) (alertnotifier.Sender, int) {
	if opts.cfg.DryRun {
		return nil, exitOK
	}
	if strings.TrimSpace(opts.apiKey) == "" {
		_, _ = fmt.Fprintf(stderr, "ERROR: %s is empty; refusing to run without a delivery path (use --dry-run to inspect)\n", apiKeyEnv)
		return nil, exitUsage
	}
	client := notify.NewResendClient(notify.ResendConfig{
		APIKey:   opts.apiKey,
		FromAddr: opts.cfg.From,
		Timeout:  opts.cfg.HTTPTimeout,
		HTTPDoer: &http.Client{Timeout: opts.cfg.HTTPTimeout},
	}).WithMetrics(metrics.NewRegistry(nil))
	return client, exitOK
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// splitRecipients parses a comma-separated recipient list, dropping blanks.
func splitRecipients(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
