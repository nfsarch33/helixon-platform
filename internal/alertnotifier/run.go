// runx-public-repo-gate: allow-file fleet_host_alias,network_topology

package alertnotifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
)

// DefaultFrom is Resend's shared onboarding sender.
//
// The repo's historical default (ops@cylrl.com.au) has never worked: the
// Resend API rejects it with HTTP 403 "The cylrl.com.au domain is not
// verified". Until a domain is verified, onboarding@resend.dev is the only
// address that delivers. It is a flag/env default, not a constant in the
// send path, so verifying a domain needs no code change.
const DefaultFrom = "onboarding@resend.dev"

// DefaultTo is the Resend account owner. On the free tier the vendor
// refuses every other recipient, so this is not a preference — it is the
// only address that can currently receive.
const DefaultTo = "jaslian@gmail.com"

// DefaultJobID attributes this component's spend per ADR-0023.
const DefaultJobID = "alert-notifier"

// Sender is the minimal notification sink. *notify.ResendClient and
// *notify.Dispatcher both satisfy it. This package deliberately never
// holds the API key itself: it only ever hands over a rendered Email.
type Sender interface {
	Send(ctx context.Context, m notify.Email) error
}

// Config is one run's configuration.
type Config struct {
	AlertmanagerURL  string
	StatePath        string
	TextfilePath     string
	From             string
	To               []string
	JobID            string
	RenotifyInterval time.Duration
	HTTPTimeout      time.Duration
	MaxBodyBytes     int64
	DryRun           bool

	// HTTPClient polls Alertmanager (not the vendor). Defaults to an
	// *http.Client bounded by HTTPTimeout.
	HTTPClient HTTPDoer
	// Sender delivers the digest. Required unless DryRun.
	Sender Sender
	// SecondarySender is the always-on non-email tier: it receives the
	// same digest as Sender on every change. v18856 exists because a
	// quota-limited email vendor must not be the only wire urgent HITL
	// items travel on. nil disables the tier.
	SecondarySender Sender
	// UrgentSender receives the digest only when UrgentWhen says the
	// change is urgent (critical severity). nil disables the tier.
	UrgentSender Sender
	// UrgentWhen gates UrgentSender. nil keeps the tier off even when a
	// sender is configured, so wiring a sender is never a behaviour
	// change by accident.
	UrgentWhen func(Change) bool
	// Now is injectable so renotify windows are testable without sleeping.
	Now func() time.Time

	Stdout io.Writer
	Stderr io.Writer
}

// Result is everything one run observed and did.
type Result struct {
	ActiveAlerts int
	Change       Change
	Digest       Digest
	Sent         bool
	SendFailures int
	// SecondarySent and UrgentSent record which non-email tiers
	// delivered, so the audit line alone shows whether urgent items
	// reached the channels the operator actually watches.
	SecondarySent bool
	UrgentSent    bool
	Metrics       Metrics
	StateNote     string
	DryRun        bool
}

// withDefaults fills the zero values so callers only set what they mean.
// It deliberately takes and returns a value: the caller's Config is never
// mutated, which is what makes a Config literal safe to reuse.
//
//nolint:gocritic // hugeParam: value semantics are the point; this runs once per process
func (c Config) withDefaults() Config {
	if c.AlertmanagerURL == "" {
		c.AlertmanagerURL = DefaultAlertmanagerURL
	}
	if c.StatePath == "" {
		c.StatePath = DefaultStatePath
	}
	if c.TextfilePath == "" {
		c.TextfilePath = DefaultTextfilePath
	}
	if c.From == "" {
		c.From = DefaultFrom
	}
	if len(c.To) == 0 {
		c.To = []string{DefaultTo}
	}
	if c.JobID == "" {
		c.JobID = DefaultJobID
	}
	if c.RenotifyInterval <= 0 {
		c.RenotifyInterval = DefaultRenotifyInterval
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = DefaultHTTPTimeout
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.HTTPTimeout}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Stdout == nil {
		c.Stdout = io.Discard
	}
	if c.Stderr == nil {
		c.Stderr = io.Discard
	}
	return c
}

// Run executes exactly one poll-diff-notify cycle.
//
// It is safe to call on a schedule with no coordination: all cross-run
// memory is the state file, and the state file is only advanced once a
// report has actually been delivered.
//
//nolint:gocritic // hugeParam: the public entry point takes a Config literal by value
func Run(ctx context.Context, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	now := cfg.Now()

	prev, note := LoadState(cfg.StatePath)
	res := Result{StateNote: note, DryRun: cfg.DryRun}

	alerts, err := pollAlerts(ctx, &cfg)
	if err != nil {
		res.Metrics = Metrics{
			LastRunUnix:     prev.LastRunUnix,
			LastSuccessUnix: prev.LastSuccessUnix,
			ActiveAlerts:    len(prev.Active),
		}
		return res, errors.Join(err, finish(&cfg, &res, nil))
	}

	res.ActiveAlerts = len(alerts)
	ch, next := Diff(prev, alerts, now, cfg.RenotifyInterval)
	next.LastRunUnix = now.Unix()
	res.Change = ch
	res.Digest = Render(ch, prev, now, cfg.AlertmanagerURL)
	res.Metrics = Metrics{
		LastRunUnix:     now.Unix(),
		LastSuccessUnix: prev.LastSuccessUnix,
		ActiveAlerts:    len(alerts),
	}

	if ch.Empty() {
		// Nothing changed: send nothing, but still record the state and
		// the heartbeat so a quiet fleet is distinguishable from a dead
		// notifier.
		return res, finish(&cfg, &res, &next)
	}
	if cfg.DryRun {
		renderPreview(&cfg, &res)
		return res, finish(&cfg, &res, nil)
	}
	sendErr := deliver(ctx, &cfg, &res, &next, now)
	finishErr := finish(&cfg, &res, statePtr(&res, &next))
	return res, errors.Join(sendErr, finishErr)
}

// pollAlerts fetches and filters the notifiable alerts under a hard
// per-poll deadline.
func pollAlerts(ctx context.Context, cfg *Config) ([]Alert, error) {
	pollCtx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
	defer cancel()
	raw, err := FetchAlerts(pollCtx, cfg.HTTPClient, cfg.AlertmanagerURL, cfg.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	return NotifiableAlerts(raw), nil
}

// deliver hands the digest to every configured tier and records the
// outcome. v18856: a digest counts as delivered when ANY tier accepts it,
// so one vendor outage (the live Resend 403) can no longer black-hole the
// report; per-tier failures are still counted for the audit trail. On
// total failure the caller must NOT persist the new state, so the next
// run retries the same report instead of losing it.
func deliver(ctx context.Context, cfg *Config, res *Result, next *State, now time.Time) error {
	if cfg.Sender == nil {
		res.SendFailures = 1
		res.Metrics.SendFailures = 1
		return errors.New("no sender configured and --dry-run not set")
	}
	email := notify.Email{
		To:             cfg.To,
		Subject:        res.Digest.Subject,
		HTMLBody:       res.Digest.HTML,
		TextBody:       res.Digest.Text,
		IdempotencyKey: res.Digest.IdempotencyKey,
		JobID:          cfg.JobID,
	}

	var errs []error
	delivered := false

	if err := cfg.Sender.Send(ctx, email); err != nil {
		res.SendFailures++
		errs = append(errs, fmt.Errorf("email: %w", err))
	} else {
		delivered = true
	}

	if cfg.SecondarySender != nil {
		if err := cfg.SecondarySender.Send(ctx, email); err != nil {
			res.SendFailures++
			errs = append(errs, fmt.Errorf("secondary: %w", err))
		} else {
			delivered = true
			res.SecondarySent = true
		}
	}

	if cfg.UrgentSender != nil && cfg.UrgentWhen != nil && cfg.UrgentWhen(res.Change) {
		if err := cfg.UrgentSender.Send(ctx, email); err != nil {
			res.SendFailures++
			errs = append(errs, fmt.Errorf("urgent: %w", err))
		} else {
			delivered = true
			res.UrgentSent = true
		}
	}

	res.Metrics.SendFailures = res.SendFailures
	if !delivered {
		return fmt.Errorf("send alert digest: %w", errors.Join(errs...))
	}
	res.Sent = true
	next.LastSuccessUnix = now.Unix()
	res.Metrics.LastSuccessUnix = now.Unix()
	return nil
}

// statePtr returns the state to persist: none when a send was attempted
// and every tier failed. Runs that delivered on at least one tier persist
// (their failures are per-channel, already counted in the audit), and
// quiet runs with no change always persist.
func statePtr(res *Result, next *State) *State {
	if !res.Sent && res.SendFailures > 0 {
		return nil
	}
	return next
}

// finish performs the always-run side effects: persist state when the
// caller supplied one, export the textfile metrics, and emit the audit
// record. In dry-run mode nothing is written to disk — an operator
// inspecting the digest must not perturb production state.
func finish(cfg *Config, res *Result, next *State) error {
	var errs []error
	if !cfg.DryRun && next != nil {
		if err := SaveState(cfg.StatePath, *next); err != nil {
			errs = append(errs, err)
		}
	}
	if !cfg.DryRun {
		// Written on every completed run — including runs that sent
		// nothing and runs whose send failed. A stale-but-plausible
		// metrics file is exactly the failure mode this replaces, so the
		// values above are carried forward honestly rather than refreshed.
		if err := WriteMetrics(cfg.TextfilePath, res.Metrics); err != nil {
			errs = append(errs, err)
		}
	}
	if err := emitAudit(cfg, res); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// renderPreview prints the rendered email to stderr so the operator can
// read it, leaving stdout as a clean NDJSON stream.
func renderPreview(cfg *Config, res *Result) {
	_, _ = fmt.Fprint(cfg.Stderr, "----- rendered digest (dry-run, not sent) -----\n")
	_, _ = fmt.Fprintf(cfg.Stderr, "From:    %s\n", cfg.From)
	_, _ = fmt.Fprintf(cfg.Stderr, "To:      %v\n", cfg.To)
	_, _ = fmt.Fprintf(cfg.Stderr, "Subject: %s\n\n%s", res.Digest.Subject, res.Digest.Text)
	_, _ = fmt.Fprint(cfg.Stderr, "----- end rendered digest -----\n")
}

// emitAudit writes one NDJSON audit line to stdout. It carries counts,
// identifiers, and body lengths — never a credential and never the body
// itself.
func emitAudit(cfg *Config, res *Result) error {
	rec := map[string]any{
		"ts":               cfg.Now().UTC().Format(time.RFC3339),
		"event":            "alert_notifier_run",
		"alertmanager_url": cfg.AlertmanagerURL,
		"active_alerts":    res.ActiveAlerts,
		"new_firing":       len(res.Change.NewFiring),
		"resolved":         len(res.Change.Resolved),
		"renotify":         len(res.Change.Renotify),
		"idempotency_key":  res.Digest.IdempotencyKey,
		"subject":          res.Digest.Subject,
		"from":             cfg.From,
		"to":               cfg.To,
		"text_len":         len(res.Digest.Text),
		"html_len":         len(res.Digest.HTML),
		"sent":             res.Sent,
		"send_failures":    res.SendFailures,
		"secondary_sent":   res.SecondarySent,
		"urgent_sent":      res.UrgentSent,
		"dry_run":          res.DryRun,
		"state_path":       cfg.StatePath,
		"textfile_path":    cfg.TextfilePath,
		"job_id":           cfg.JobID,
	}
	if res.StateNote != "" {
		rec["state_note"] = res.StateNote
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	if _, err := fmt.Fprintf(cfg.Stdout, "%s\n", raw); err != nil {
		return fmt.Errorf("write audit record: %w", err)
	}
	return nil
}
