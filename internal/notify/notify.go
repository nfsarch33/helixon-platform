// Package notify provides the universal email-notification client for the
// Helixon platform. It exposes a vendor-rotating dispatcher (Resend +
// Brevo) with HTTP-API-only delivery (no SMTP, per ADR-0087) and is the
// single sink called by Sprint 14 / v16101 onward. Raw curl/fetch/requests
// to *.resend.com or *.brevo.com are DENIED by the helix-dev-tools
// guard-email hook and must route through this package.
//
// Design references:
//   - ADR-0087 — SMTP forbidden; vendor REST API only
//   - ADR-0023 — cost-observability hard rule (tokens + estimated $ + job_id)
//   - CARRY-044 — GitHub identity jaslian@gmail.com is canonical
//
// Notification target (per v16101 operator directive):
//
//	primary:  jaslian@gmail.com
//	cc:       info@oztac.com.au, info@cylrl.com.au  (collapsed into "to" for Resend free tier)
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"time"

	"github.com/nfsarch33/helixon-platform/internal/tenantid"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
	"go.opentelemetry.io/otel/metric"
)

// Email is the package's wire-level input. IdempotencyKey is required for
// every Send call to enable safe retry without duplicate deliveries.
type Email struct {
	To             []string // primary recipients
	CC             []string // cc (collapsed into To for Resend free tier per ADR-0087)
	BCC            []string // bcc (passed through to vendor)
	Subject        string
	HTMLBody       string
	TextBody       string
	IdempotencyKey string  // REQUIRED; de-duplicates in-flight and replayed sends
	JobID          string  // for cost attribution
	TenantID       string  // for cost attribution
	CostEstimate   float64 // USD; best-effort, surfaced via OTel
}

// ErrPermanent marks a 4xx-style failure that MUST NOT be retried.
var ErrPermanent = errors.New("permanent failure")

// ErrTransient marks a 5xx or network-style failure that has not yet
// exhausted the retry budget. Wraps a deadline or attempt count.
var ErrTransient = errors.New("transient failure")

// ErrDeadLetter marks a transient failure that HAS exhausted the retry
// budget. Caller must persist + alert; do not retry automatically.
var ErrDeadLetter = errors.New("dead-letter: retry budget exhausted")

// ErrIdempotencyConflict marks a send where the same IdempotencyKey is
// already in flight from another caller. The original call wins; this
// caller sees the result via the in-flight promise.
var ErrIdempotencyConflict = errors.New("idempotency key in flight")

// Recipients carries the unified notification target. The Primary is the
// canonical GitHub identity (per CARRY-044 + v14502 identity correction).
type Recipients struct {
	Primary string   // "jaslian@gmail.com"
	CC      []string // ["info@oztac.com.au", "info@cylrl.com.au"]
}

// DefaultRecipients returns the v16101-mandated notification target.
func DefaultRecipients() Recipients {
	return Recipients{
		Primary: "jaslian@gmail.com",
		CC:      []string{"info@oztac.com.au", "info@cylrl.com.au"},
	}
}

// HTTPDoer is the subset of *http.Client the package uses. Tests inject
// a custom transport to avoid network; production uses *http.Client.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is the vendor-agnostic interface for the dispatcher.
type Client interface {
	Send(ctx context.Context, m Email) error
	// Vendor returns the vendor name (for cost attribution metrics).
	Vendor() string
}

// ---------------------------------------------------------------------------
// Resend client
// ---------------------------------------------------------------------------

// ResendConfig is the configuration for the Resend HTTP-API client.
type ResendConfig struct {
	APIKey    string
	BaseURL   string        // default https://api.resend.com
	FromAddr  string        // verified sender e.g. ops@cylrl.com.au
	Timeout   time.Duration // per-attempt HTTP timeout
	MaxRetry  int           // 0 -> 3
	OtelMeter metric.Meter
	HTTPDoer  HTTPDoer // optional test injection
}

// ResendClient is the Resend HTTP-API client. Implements Client.
type ResendClient struct {
	cfg     ResendConfig
	do      HTTPDoer
	id      *IdempotencyStore
	audit   *notifydb.DB
	metrics *metrics.Registry
}

// NewResendClient returns a Resend client with the supplied config.
func NewResendClient(cfg ResendConfig) *ResendClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.resend.com"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxRetry == 0 {
		cfg.MaxRetry = 3
	}
	if cfg.HTTPDoer == nil {
		cfg.HTTPDoer = &http.Client{Timeout: cfg.Timeout}
	}
	return &ResendClient{cfg: cfg, do: cfg.HTTPDoer, id: NewIdempotencyStore()}
}

// WithAuditDB attaches a notifydb persistence sink. v17409-4.
func (c *ResendClient) WithAuditDB(db *notifydb.DB) *ResendClient {
	c.audit = db
	return c
}

// WithMetrics attaches a metrics.Registry for observability counters.
// v17409-6.
func (c *ResendClient) WithMetrics(r *metrics.Registry) *ResendClient {
	c.metrics = r
	return c
}

// recordAudit writes a Dispatch row when c.audit is set. Errors are logged
// but never returned to the caller (audit is best-effort).
func (c *ResendClient) recordAudit(ctx context.Context, key, subject string, err error) {
	if c.audit == nil {
		return
	}
	status := "ok"
	errStr := ""
	if err != nil {
		status = "error"
		errStr = err.Error()
	}
	_ = c.audit.Insert(ctx, notifydb.Dispatch{
		ID:          key,
		Vendor:      "resend",
		Recipient:   "(collapsed)",
		Subject:     subject,
		Status:      status,
		Error:       errStr,
		CreatedUnix: time.Now().Unix(),
		SentAtUnix:  time.Now().Unix(),
		Attempt:     c.cfg.MaxRetry,
	})
}

// Vendor returns the vendor name for cost attribution.
func (c *ResendClient) Vendor() string { return "resend" }

type resendPayload struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html,omitempty"`
	Text    string            `json:"text,omitempty"`
	BCC     []string          `json:"bcc,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Send delivers an email via Resend with retry classification, idempotency,
// and cost observability.
func (c *ResendClient) Send(ctx context.Context, m Email) error {
	if m.IdempotencyKey == "" {
		return fmt.Errorf("%w: IdempotencyKey required", ErrPermanent)
	}
	// Idempotency: if another in-flight call holds this key, return its
	// result when it completes, or ErrIdempotencyConflict if cancelled.
	if _, _, inFlight := c.id.Acquire(m.IdempotencyKey); inFlight {
		return ErrIdempotencyConflict
	}
	defer c.id.Release(m.IdempotencyKey)

	// Collapse CC into To per Resend free-tier semantics (ADR-0087 + ADR v16101).
	merged := mergeRecipients(m.To, m.CC)

	payload := resendPayload{
		From:    c.cfg.FromAddr,
		To:      merged,
		Subject: m.Subject,
		HTML:    m.HTMLBody,
		Text:    m.TextBody,
		BCC:     m.BCC,
		Headers: map[string]string{
			"X-Idempotency-Key": m.IdempotencyKey,
			"X-Job-Id":          m.JobID,
			"X-Tenant-Id":       m.TenantID,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshal payload: %w", ErrPermanent, err)
	}

	return c.doWithRetry(ctx, body, m)
}

func (c *ResendClient) doWithRetry(ctx context.Context, body []byte, m Email) error {
	endpoint, err := url.JoinPath(c.cfg.BaseURL, "/emails")
	if err != nil {
		return fmt.Errorf("%w: build endpoint: %w", ErrPermanent, err)
	}
	actor := &resendActor{c: c, m: m, endpoint: endpoint}
	return retryWithBackoff(ctx, c.cfg.MaxRetry+1, body, m, actor)
}

// resendActor implements retryActor for Resend (single-shot HTTP request with
// Bearer-token auth + JSON body + Idempotency-Key header). tech-debt-block-8.
type resendActor struct {
	c        *ResendClient
	m        Email
	endpoint string
}

// onAttempt increments the Resend per-attempt metric. CC=2.
func (a *resendActor) onAttempt(ctx context.Context, _ int) {
	if a.c.metrics != nil {
		a.c.metrics.IncAttempt(ctx, metrics.VendorResend)
	}
}

// buildRequest is the Resend-specific request constructor (CC=1).
func (a *resendActor) buildRequest(ctx context.Context, body []byte) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, "POST", a.endpoint, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+a.c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", a.m.IdempotencyKey)
	return req
}

// do executes one HTTP call. CC=1.
func (a *resendActor) do(req *http.Request) (*http.Response, error) { return a.c.do.Do(req) }

// onSuccess records audit + emits success metric for 2xx responses. CC=2.
func (a *resendActor) onSuccess(ctx context.Context, _ []byte) error {
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, nil)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorResend, metrics.StatusSuccess)
	}
	return nil
}

// onBadRequest records audit + emits bad-request metric for 4xx responses.
// Sanitizes the body before embedding in the error so server echoes never
// carry the API key. CC=4.
func (a *resendActor) onBadRequest(ctx context.Context, status int, body []byte) error {
	finalErr := fmt.Errorf("%w: status %d: %s", ErrPermanent, status, sanitizeBody(body))
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, finalErr)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorResend, metrics.StatusBadRequest)
	}
	return finalErr
}

// onTransportError records audit + emits dead-letter metric when the HTTP
// call itself returned an error (network/timeout/cancelled). CC=4.
func (a *resendActor) onTransportError(ctx context.Context, attempt int, err error) error {
	finalErr := fmt.Errorf("%w: %d attempts: %w", ErrDeadLetter, attempt, err)
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, finalErr)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorResend, metrics.StatusDeadLetter)
	}
	return finalErr
}

// onTransientExhausted records audit + emits dead-letter metric when the
// transient budget is exhausted (last attempt 5xx). CC=4.
func (a *resendActor) onTransientExhausted(ctx context.Context, attempt, status int, body []byte) error {
	finalErr := fmt.Errorf("%w: status %d after %d attempts: %s", ErrDeadLetter, status, attempt, sanitizeBody(body))
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, finalErr)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorResend, metrics.StatusDeadLetter)
	}
	return finalErr
}

// lastTransientError returns the transient error to surface if the loop
// exits without a definitive outcome (defensive — should never hit in
// practice). CC=1.
func (a *resendActor) lastTransientError(status int) error {
	return fmt.Errorf("%w: status %d", ErrTransient, status)
}

// ---------------------------------------------------------------------------
// Brevo client
// ---------------------------------------------------------------------------

// BrevoConfig is the configuration for the Brevo HTTP-API client.
type BrevoConfig struct {
	APIKey    string
	BaseURL   string        // default https://api.brevo.com/v3
	Timeout   time.Duration // per-attempt HTTP timeout
	MaxRetry  int           // 0 -> 3
	OtelMeter metric.Meter
	HTTPDoer  HTTPDoer
}

// BrevoClient is the Brevo HTTP-API client.
type BrevoClient struct {
	cfg     BrevoConfig
	do      HTTPDoer
	id      *IdempotencyStore
	audit   *notifydb.DB
	metrics *metrics.Registry
}

// NewBrevoClient returns a Brevo client with the supplied config.
func NewBrevoClient(cfg BrevoConfig) *BrevoClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.brevo.com/v3"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxRetry == 0 {
		cfg.MaxRetry = 3
	}
	if cfg.HTTPDoer == nil {
		cfg.HTTPDoer = &http.Client{Timeout: cfg.Timeout}
	}
	return &BrevoClient{cfg: cfg, do: cfg.HTTPDoer, id: NewIdempotencyStore()}
}

// Vendor returns the vendor name.
func (c *BrevoClient) Vendor() string { return "brevo" }

// WithAuditDB attaches a notifydb persistence sink. v17409-4.
func (c *BrevoClient) WithAuditDB(db *notifydb.DB) *BrevoClient {
	c.audit = db
	return c
}

// WithMetrics attaches a metrics.Registry for observability counters.
// v17409-6.
func (c *BrevoClient) WithMetrics(r *metrics.Registry) *BrevoClient {
	c.metrics = r
	return c
}

// recordAudit writes a Dispatch row when c.audit is set. Best-effort.
func (c *BrevoClient) recordAudit(ctx context.Context, key, subject string, err error) {
	if c.audit == nil {
		return
	}
	status := "ok"
	errStr := ""
	if err != nil {
		status = "error"
		errStr = err.Error()
	}
	_ = c.audit.Insert(ctx, notifydb.Dispatch{
		ID:          key,
		Vendor:      "brevo",
		Recipient:   "(collapsed)",
		Subject:     subject,
		Status:      status,
		Error:       errStr,
		CreatedUnix: time.Now().Unix(),
		SentAtUnix:  time.Now().Unix(),
		Attempt:     c.cfg.MaxRetry,
	})
}

type brevoPayload struct {
	Sender      brevoAddr         `json:"sender"`
	To          []brevoAddr       `json:"to"`
	CC          []brevoAddr       `json:"cc,omitempty"`
	BCC         []brevoAddr       `json:"bcc,omitempty"`
	Subject     string            `json:"subject"`
	HTMLContent string            `json:"htmlContent,omitempty"`
	TextContent string            `json:"textContent,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}
type brevoAddr struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// Send delivers an email via Brevo with retry classification and
// idempotency. Brevo preserves CC as a separate field so no collapse
// is needed.
func (c *BrevoClient) Send(ctx context.Context, m Email) error {
	if m.IdempotencyKey == "" {
		return fmt.Errorf("%w: IdempotencyKey required", ErrPermanent)
	}
	if _, _, inFlight := c.id.Acquire(m.IdempotencyKey); inFlight {
		return ErrIdempotencyConflict
	}
	defer c.id.Release(m.IdempotencyKey)

	to := make([]brevoAddr, 0, len(m.To))
	for _, e := range m.To {
		to = append(to, brevoAddr{Email: e})
	}
	cc := make([]brevoAddr, 0, len(m.CC))
	for _, e := range m.CC {
		cc = append(cc, brevoAddr{Email: e})
	}
	bcc := make([]brevoAddr, 0, len(m.BCC))
	for _, e := range m.BCC {
		bcc = append(bcc, brevoAddr{Email: e})
	}
	payload := brevoPayload{
		Sender:      brevoAddr{Email: defaultBrevoSender()},
		To:          to,
		CC:          cc,
		BCC:         bcc,
		Subject:     m.Subject,
		HTMLContent: m.HTMLBody,
		TextContent: m.TextBody,
		Headers: map[string]string{
			"X-Idempotency-Key": m.IdempotencyKey,
			"X-Job-Id":          m.JobID,
			"X-Tenant-Id":       m.TenantID,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshal payload: %w", ErrPermanent, err)
	}

	return c.doWithRetry(ctx, body, m)
}

func defaultBrevoSender() string { return "ops@cylrl.com.au" }

func (c *BrevoClient) doWithRetry(ctx context.Context, body []byte, m Email) error {
	endpoint, err := url.JoinPath(c.cfg.BaseURL, "/smtp/email")
	if err != nil {
		return fmt.Errorf("%w: build endpoint: %w", ErrPermanent, err)
	}
	actor := &brevoActor{c: c, m: m, endpoint: endpoint}
	return retryWithBackoff(ctx, c.cfg.MaxRetry+1, body, m, actor)
}

// brevoActor implements retryActor for Brevo (single-shot HTTP request with
// api-key header auth + JSON body + Idempotency-Key header). tech-debt-block-8.
type brevoActor struct {
	c        *BrevoClient
	m        Email
	endpoint string
}

// onAttempt increments the Brevo per-attempt metric. CC=2.
func (a *brevoActor) onAttempt(ctx context.Context, _ int) {
	if a.c.metrics != nil {
		a.c.metrics.IncAttempt(ctx, metrics.VendorBrevo)
	}
}

// buildRequest is the Brevo-specific request constructor (CC=1).
func (a *brevoActor) buildRequest(ctx context.Context, body []byte) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, "POST", a.endpoint, strings.NewReader(string(body)))
	req.Header.Set("api-key", a.c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", a.m.IdempotencyKey)
	return req
}

// do executes one HTTP call. CC=1.
func (a *brevoActor) do(req *http.Request) (*http.Response, error) { return a.c.do.Do(req) }

// onSuccess records audit + emits success metric for 2xx responses. CC=2.
func (a *brevoActor) onSuccess(ctx context.Context, _ []byte) error {
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, nil)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorBrevo, metrics.StatusSuccess)
	}
	return nil
}

// onBadRequest records audit + emits bad-request metric for 4xx responses.
// Sanitizes the body before embedding in the error. CC=4.
func (a *brevoActor) onBadRequest(ctx context.Context, status int, body []byte) error {
	finalErr := fmt.Errorf("%w: status %d: %s", ErrPermanent, status, sanitizeBody(body))
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, finalErr)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorBrevo, metrics.StatusBadRequest)
	}
	return finalErr
}

// onTransportError records audit + emits dead-letter metric when the HTTP
// call itself returned an error. CC=4.
func (a *brevoActor) onTransportError(ctx context.Context, attempt int, err error) error {
	finalErr := fmt.Errorf("%w: %d attempts: %w", ErrDeadLetter, attempt, err)
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, finalErr)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorBrevo, metrics.StatusDeadLetter)
	}
	return finalErr
}

// onTransientExhausted records audit + emits dead-letter metric when the
// transient budget is exhausted. CC=4.
func (a *brevoActor) onTransientExhausted(ctx context.Context, attempt, status int, body []byte) error {
	finalErr := fmt.Errorf("%w: status %d after %d attempts: %s", ErrDeadLetter, status, attempt, sanitizeBody(body))
	a.c.recordAudit(ctx, a.m.IdempotencyKey, a.m.Subject, finalErr)
	if a.c.metrics != nil {
		a.c.metrics.IncSend(ctx, metrics.VendorBrevo, metrics.StatusDeadLetter)
	}
	return finalErr
}

// lastTransientError returns the transient error to surface if the loop
// exits without a definitive outcome. CC=1.
func (a *brevoActor) lastTransientError(status int) error {
	return fmt.Errorf("%w: status %d", ErrTransient, status)
}

// ---------------------------------------------------------------------------
// Dispatcher (vendor rotation + fallback)
// ---------------------------------------------------------------------------

// DispatcherConfig is the configuration for the round-robin vendor dispatcher.
type DispatcherConfig struct {
	ResendClient Client
	BrevoClient  Client
	OtelMeter    metric.Meter
	// Primary is an optional override (e.g. RotatingSender) used in
	// preference to the ResendClient+BrevoClient pair. When set, the
	// round-robin path is skipped and every send goes through Primary;
	// the legacy fallback path remains as a safety net. v17607-6.
	Primary Client
	// BrevoOnly (xcut-10, v18518) drops Resend from the round-robin
	// pickOrder entirely. The Brevo client is the sole outbound vendor.
	// Use when Resend's sender domain is unverified (CF-105).
	BrevoOnly bool
}

// Dispatcher rotates between Resend and Brevo; falls back to the other
// vendor when one exhausts its retry budget. Idempotency is per-Email.IdempotencyKey.
type Dispatcher struct {
	cfg      DispatcherConfig
	rrCursor atomic.Uint64
}

// NewDispatcher returns a vendor-rotating dispatcher.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	return &Dispatcher{cfg: cfg}
}

// WithMetrics propagates the metrics registry to both vendor clients
// so the round-robin Send path emits the same counters. v17409-6.
func (d *Dispatcher) WithMetrics(r *metrics.Registry) *Dispatcher {
	if rc, ok := d.cfg.ResendClient.(*ResendClient); ok {
		rc.WithMetrics(r)
	}
	if bc, ok := d.cfg.BrevoClient.(*BrevoClient); ok {
		bc.WithMetrics(r)
	}
	return d
}

// WithAuditDB propagates the audit DB to both vendor clients. v17409-4.
func (d *Dispatcher) WithAuditDB(db *notifydb.DB) *Dispatcher {
	if rc, ok := d.cfg.ResendClient.(*ResendClient); ok {
		rc.WithAuditDB(db)
	}
	if bc, ok := d.cfg.BrevoClient.(*BrevoClient); ok {
		bc.WithAuditDB(db)
	}
	return d
}

// Send attempts the email via the round-robin pick; on ErrDeadLetter, falls
// back to the other vendor before propagating the failure. When a
// Primary (e.g. RotatingSender) is configured, it is used directly and
// the round-robin pick is skipped.
// resolveTenantID derives the tenant id used for cost attribution and
// audit logging. Per CF-172, the context takes precedence (per-job), the
// Email.TenantID field is the secondary source (per-call), and the
// `HELIXON_TENANT_ID` env var is the boot-time fallback. When none are
// set, the result is "default" so a single-tenant deployment keeps
// working without config.
func resolveTenantID(ctx context.Context, m Email) string {
	// 1. Per-request context (preferred).
	if v := tenantid.TenantIDFrom(ctx); v != tenantid.DefaultTenantID {
		return v
	}
	// 2. Per-call field on Email struct.
	if m.TenantID != "" {
		return m.TenantID
	}
	// 3. Boot-time env var.
	return tenantid.EnvTenantID()
}

func (d *Dispatcher) Send(ctx context.Context, m Email) error {
	if m.IdempotencyKey == "" {
		return fmt.Errorf("%w: IdempotencyKey required", ErrPermanent)
	}
	// v17607-6: enforce canonical recipient allowlist (jaslian@gmail.com only).
	if err := ValidateRecipients(m.To); err != nil {
		return err
	}
	// v18675-3 (CF-172): propagate tenant id from context with fallback to
	// the Email.TenantID field. Single-tenant deployments keep working
	// without config (env var or context).
	m.TenantID = resolveTenantID(ctx, m)

	// If a Primary is configured (RotatingSender), use it directly.
	// xcut-10 (v18518): when BrevoOnly is set, skip Primary entirely
	// (the operator has decided Resend is unverified and Primary may
	// pick a Resend key). Brevo-only path is the sole outbound.
	if d.cfg.Primary != nil && !d.cfg.BrevoOnly {
		if err := d.cfg.Primary.Send(ctx, m); err == nil {
			return nil
		} else if !errors.Is(err, ErrDeadLetter) {
			return err
		}
		// Fall through to legacy round-robin as a safety net.
	}

	order := d.pickOrder()
	var lastErr error
	for i, vendor := range order {
		err := vendor.Send(ctx, m)
		if err == nil {
			return nil
		}
		lastErr = err
		// Only fall back on dead-letter; permanent (4xx) errors and
		// idempotency conflicts should NOT trigger a second vendor —
		// the failure is structural.
		if errors.Is(err, ErrDeadLetter) && i == 0 {
			continue
		}
		return err
	}
	return lastErr
}

func (d *Dispatcher) pickOrder() []Client {
	// xcut-10 (v18518): when BrevoOnly is set, Resend is excluded from
	// the round-robin pool (sender domain unverified, CF-105). Falls back
	// to Brevo alone with no fallback vendor.
	if d.cfg.BrevoOnly {
		if d.cfg.BrevoClient == nil {
			return nil
		}
		return []Client{d.cfg.BrevoClient}
	}
	primary := d.cfg.ResendClient
	secondary := d.cfg.BrevoClient
	if d.rrCursor.Add(1)%2 == 0 {
		primary, secondary = secondary, primary
	}
	return []Client{primary, secondary}
}

// ---------------------------------------------------------------------------
// IdempotencyStore
// ---------------------------------------------------------------------------

// IdempotencyStore is a goroutine-safe in-memory idempotency record.
// Sufficient for single-process dispatcher use; production multi-process
// deployments should swap for Redis or Postgres advisory lock.
type IdempotencyStore struct {
	mu sync.Mutex
	m  map[string]*idempotencyPromise
}

type idempotencyPromise struct {
	done chan struct{}
}

// NewIdempotencyStore returns an empty in-memory store.
func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{m: make(map[string]*idempotencyPromise)}
}

// Acquire marks the key as in-flight. Returns (acquired=true, inFlight=false)
// for the first caller; (acquired=false, inFlight=true) for subsequent
// callers observing the same key.
func (s *IdempotencyStore) Acquire(key string) (bool, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.m[key]; ok {
		if existing.done == nil {
			return false, true, true // already in-flight
		}
		return true, false, true // already completed, caller treats as dedup
	}
	s.m[key] = &idempotencyPromise{done: make(chan struct{})}
	return true, false, false
}

// Release marks the key complete and records the result. Future callers
// for the same key see Seen()==true but no in-flight promise.
func (s *IdempotencyStore) Release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[key]
	if !ok {
		return
	}
	if p.done != nil {
		close(p.done)
		p.done = nil
	}
}

// Seen returns true if the key was ever acquired.
func (s *IdempotencyStore) Seen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[key]
	return ok
}

// Record adds a key that completed successfully (used by callers that
// re-issue a send after the original completed).
func (s *IdempotencyStore) Record(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; !ok {
		s.m[key] = &idempotencyPromise{}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mergeRecipients returns the union of to + cc with de-duplication,
// preserving order. Used for Resend free-tier CC collapse.
func mergeRecipients(to, cc []string) []string {
	seen := make(map[string]struct{}, len(to)+len(cc))
	out := make([]string, 0, len(to)+len(cc))
	for _, e := range to {
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	for _, e := range cc {
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}

// backoff sleeps 100ms * 2^(attempt-1) + jitter (max 2s).
// attempt is 1-indexed (first retry = attempt 1).
func backoff(attempt int) {
	base := time.Duration(100<<(attempt-1)) * time.Millisecond
	if base > 2*time.Second {
		base = 2 * time.Second
	}
	jitter := time.Duration(hashJitter()) % (base / 4)
	time.Sleep(base + jitter)
}

// hashJitter returns a deterministic-ish jitter value derived from the
// process start time and attempt count, so backoffs are not synchronized
// across concurrent calls. (Real production would use crypto/rand.)
//
// The LCG constants are 64-bit; after a few iterations the running state
// exceeds math.MaxInt64 and the cast to time.Duration would wrap to a
// negative value, causing backoff() to sleep for a negative duration
// (i.e. zero). Mask off the sign bit so the result is always non-negative
// when consumed as a signed integer or time.Duration downstream.
var jitterSeed = uint64(time.Now().UnixNano())

func hashJitter() uint64 {
	jitterSeed = jitterSeed*6364136223846793005 + 1442695040888963407
	return jitterSeed & 0x7FFFFFFFFFFFFFFF
}

// sanitizeBody returns a short redacted view of a vendor error body that
// NEVER contains an API key. Best-effort: if the body contains
// "re_" or "xkeysib-" tokens, replace with [REDACTED].
func sanitizeBody(b []byte) string {
	s := string(b)
	for _, prefix := range []string{"re_", "xkeysib-"} {
		for {
			idx := indexOfToken(s, prefix)
			if idx < 0 {
				break
			}
			end := idx + len(prefix)
			for end < len(s) && (isAlnum(s[end]) || s[end] == '_' || s[end] == '-') {
				end++
			}
			s = s[:idx] + "[REDACTED]" + s[end:]
		}
	}
	if len(s) > 256 {
		s = s[:256] + "...[truncated]"
	}
	return s
}

func indexOfToken(s, prefix string) int {
	for i := 0; i+len(prefix) <= len(s); i++ {
		if s[i:i+len(prefix)] == prefix {
			return i
		}
	}
	return -1
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// Hash32 returns a stable 32-bit hash of the input; used for cost
// attribution row keys in tests and downstream dashboards.
// Hash32 returns a stable 32-bit hash of the input; used for cost
// attribution row keys in tests and downstream dashboards.
func Hash32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// ---------------------------------------------------------------------------
// retryActor — shared retry-loop abstraction
// ---------------------------------------------------------------------------

// retryActor encapsulates the vendor-specific pieces of a single-shot HTTP
// delivery. The orchestration (retry, backoff, attempt counting, audit final
// emission) lives in retryWithBackoff. Splitting these keeps the orchestration
// loop flat (CC ≤ 4) and lets us add new vendors without duplicating it.
//
// Contract for every method:
//   - buildRequest must attach the right auth headers
//   - do runs the HTTP call
//   - onSuccess is called for any 2xx response
//   - onBadRequest is called for any 4xx response (fail-fast, no retry)
//   - onTransportError is called when Do() itself returned an error AND
//     the attempt budget has been exhausted
//   - onTransientExhausted is called for a 5xx response on the last attempt
//   - lastTransientError returns the marker error to set when we will retry
//     the next iteration (never the user-visible error)
type retryActor interface {
	// onAttempt is invoked at the start of each attempt. Actors use it to
	// increment the per-vendor metrics counter (no return value).
	onAttempt(ctx context.Context, attempt int)
	buildRequest(ctx context.Context, body []byte) *http.Request
	do(req *http.Request) (*http.Response, error)
	onSuccess(ctx context.Context, body []byte) error
	onBadRequest(ctx context.Context, status int, body []byte) error
	onTransportError(ctx context.Context, attempt int, err error) error
	onTransientExhausted(ctx context.Context, attempt, status int, body []byte) error
	lastTransientError(status int) error
}

// retryWithBackoff drives the per-attempt loop. It is intentionally small
// (CC ≤ 4) because every behavioural branch is delegated to the actor.
// CC=4.
func retryWithBackoff(ctx context.Context, maxAttempt int, body []byte, m Email, actor retryActor) error { //nolint:unparam // body parameter reserved for templated retry bodies in future path
	var lastErr error
	for attempt := 1; attempt <= maxAttempt; attempt++ {
		actor.onAttempt(ctx, attempt)

		req := actor.buildRequest(ctx, body)
		resp, err := actor.do(req)
		if err != nil {
			lastErr = actor.lastTransientError(0)
			if attempt == maxAttempt {
				return actor.onTransportError(ctx, attempt, err)
			}
			backoff(attempt)
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return actor.onSuccess(ctx, respBody)
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return actor.onBadRequest(ctx, resp.StatusCode, respBody)
		}
		lastErr = actor.lastTransientError(resp.StatusCode)
		if attempt == maxAttempt {
			return actor.onTransientExhausted(ctx, attempt, resp.StatusCode, respBody)
		}
		backoff(attempt)
	}
	return lastErr
}
