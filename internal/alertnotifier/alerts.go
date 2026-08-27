// runx-public-repo-gate: allow-file fleet_host_alias,network_topology

// Package alertnotifier turns Alertmanager's active-alert list into a
// human-reaching email digest.
//
// Why a poller and not a webhook receiver (v18776 design decision):
//
//   - It opens no listening port. This estate already has too much bound
//     on all interfaces; an alerter that needs an inbound socket is a new
//     attack surface for zero benefit.
//   - It needs no Alertmanager configuration change, so the live alerting
//     stack is never restarted to install it.
//   - It is crash-safe and resumable: all cross-run memory lives in one
//     atomically-written JSON state file, so a killed run simply repeats.
//
// Delivery is HTTP-API-only via internal/notify (ADR-0087 forbids SMTP;
// ADR-077 makes internal/notify the canonical sink). This package never
// constructs its own HTTP client for the vendor and never handles the API
// key: it takes a Sender and hands it a notify.Email.
package alertnotifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultAlertmanagerURL is the loopback Alertmanager this fleet runs.
const DefaultAlertmanagerURL = "http://127.0.0.1:9093"

// alertsPath is the Alertmanager v2 active-alert endpoint.
const alertsPath = "/api/v2/alerts"

// DefaultMaxBodyBytes bounds the Alertmanager response read. The live
// payload for 8 firing alerts is ~9 KiB; 4 MiB is four orders of
// magnitude of headroom and still refuses an unbounded stream.
const DefaultMaxBodyBytes int64 = 4 << 20

// DefaultHTTPTimeout bounds a single Alertmanager poll.
const DefaultHTTPTimeout = 10 * time.Second

// labelAlertname and labelSeverity are the two labels rendered as
// structure (heading and grouping) rather than as distinguishing detail.
const (
	labelAlertname = "alertname"
	labelSeverity  = "severity"
)

// severityUnknown is used when an alert carries no severity label.
const severityUnknown = "unknown"

// HTTPDoer is the subset of *http.Client this package needs. Tests inject
// an httptest-backed client; production passes an *http.Client with a
// timeout already set.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// AlertStatus is the Alertmanager v2 status sub-object.
type AlertStatus struct {
	State       string   `json:"state"`
	SilencedBy  []string `json:"silencedBy"`
	InhibitedBy []string `json:"inhibitedBy"`
}

// Alert is the subset of the Alertmanager v2 alert object this package
// reads. Unknown fields are ignored by encoding/json.
type Alert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	Fingerprint  string            `json:"fingerprint"`
	GeneratorURL string            `json:"generatorURL"`
	Status       AlertStatus       `json:"status"`
}

// Name returns the alertname label, or a placeholder when absent.
//
// The Alert accessors take a pointer receiver purely to avoid copying a
// 160-byte struct on every call in the render loops; none of them mutate.
func (a *Alert) Name() string {
	if v := a.Labels[labelAlertname]; v != "" {
		return v
	}
	return "(unnamed)"
}

// Severity returns the severity label, lower-cased, or severityUnknown.
func (a *Alert) Severity() string {
	if v := a.Labels[labelSeverity]; v != "" {
		return strings.ToLower(v)
	}
	return severityUnknown
}

// DistinguishingLabels returns the sorted "k=v" label pairs that are not
// already rendered as structure (alertname, severity). These are what let
// an operator tell two firings of the same rule apart.
func (a *Alert) DistinguishingLabels() []string {
	keys := make([]string, 0, len(a.Labels))
	for k := range a.Labels {
		if k == labelAlertname || k == labelSeverity {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+a.Labels[k])
	}
	return out
}

// Notifiable reports whether the alert should reach a human. Silenced and
// inhibited alerts (state "suppressed") deliberately do not: an operator
// who silenced a rule asked not to be told. A resolved entry in the active
// list is likewise skipped.
func (a *Alert) Notifiable() bool {
	switch strings.ToLower(a.Status.State) {
	case "suppressed", "resolved":
		return false
	default:
		return true
	}
}

// FingerprintOf returns a stable per-alert identity. Alertmanager already
// computes one over the label set; we reuse it when present so our state
// file agrees with the Alertmanager UI. When it is absent (hand-written
// fixtures, a future API change) we fall back to a sha256 over the sorted
// label set, which is order-independent by construction.
func FingerprintOf(a *Alert) string {
	if fp := strings.TrimSpace(a.Fingerprint); fp != "" {
		return fp
	}
	keys := make([]string, 0, len(a.Labels))
	for k := range a.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = io.WriteString(h, k)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, a.Labels[k])
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// FetchAlerts polls Alertmanager for the current alert list.
//
// Every network path in this repo gets a hard timeout and a bounded body
// read at birth: the timeout is the caller's context (Run always sets
// one), and maxBody caps the read. A body larger than maxBody is an error,
// not a silent truncation — a truncated JSON document would either fail to
// parse or, worse, parse into a short alert list and be read as "alerts
// resolved".
func FetchAlerts(ctx context.Context, doer HTTPDoer, baseURL string, maxBody int64) ([]Alert, error) {
	if baseURL == "" {
		baseURL = DefaultAlertmanagerURL
	}
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	endpoint, err := url.JoinPath(baseURL, alertsPath)
	if err != nil {
		return nil, fmt.Errorf("build alertmanager endpoint: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build alertmanager request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll alertmanager: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readBounded(resp.Body, maxBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alertmanager returned status %d: %s", resp.StatusCode, snippet(body))
	}
	var alerts []Alert
	if err := json.Unmarshal(body, &alerts); err != nil {
		return nil, fmt.Errorf("decode alertmanager response: %w", err)
	}
	return alerts, nil
}

// readBounded reads at most maxBody bytes and fails if the source had
// more, rather than returning a silently truncated document.
func readBounded(r io.Reader, maxBody int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read alertmanager body: %w", err)
	}
	if int64(len(body)) > maxBody {
		return nil, fmt.Errorf("alertmanager body exceeds %d bytes", maxBody)
	}
	return body, nil
}

// snippet bounds an error-embedded response body so a hostile or verbose
// endpoint cannot blow up a log line.
func snippet(b []byte) string {
	const limit = 256
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "...[truncated]"
	}
	return s
}

// NotifiableAlerts filters a raw Alertmanager list down to the alerts that
// should reach a human.
func NotifiableAlerts(in []Alert) []Alert {
	out := make([]Alert, 0, len(in))
	for i := range in {
		if in[i].Notifiable() {
			out = append(out, in[i])
		}
	}
	return out
}
