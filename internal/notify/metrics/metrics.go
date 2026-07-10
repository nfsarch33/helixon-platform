// Package metrics holds the OTel counters that surface vendor rotation
// and dispatch outcomes for downstream dashboards.
//
// v17409-6: rotate the observability surface from "OtelMeter field but
// unused" to a real set of counters:
//   - notify_send_total{vendor,status}  // per send outcome
//   - notify_send_duration_seconds_sum  // cumulative latency
//   - notify_send_attempts_total        // per retry attempt
//
// The counter values are exposed through a tiny in-process registry that
// the tests can read directly. The OTel meter (if supplied) receives the
// same increments so an OpenTelemetry collector can ship to Prometheus.
package metrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Status is the outcome of a single send attempt.
type Status string

const (
	StatusSuccess    Status = "ok"
	StatusBadRequest Status = "bad_request" // 4xx
	StatusTransient  Status = "transient"   // 5xx
	StatusDeadLetter Status = "dead_letter" // retries exhausted
)

// Vendor is the email provider.
type Vendor string

const (
	VendorResend   Vendor = "resend"
	VendorBrevo    Vendor = "brevo"
	VendorTelegram Vendor = "telegram"
)

// Registry is the in-process counter store. It is safe for concurrent
// use; one instance is shared by all vendor clients.
type Registry struct {
	mu       sync.RWMutex
	counter  map[counterKey]int64
	durSum   map[counterKey]time.Duration
	attempts map[vendorKey]int64

	meter  metric.Meter
	intCnt metric.Int64Counter
	durHst metric.Float64Histogram
	attCnt metric.Int64Counter
}

type counterKey struct {
	Vendor Vendor
	Status Status
}

func (k counterKey) toSendKey() SendKey { return SendKey(k) }

type vendorKey struct {
	Vendor Vendor
}

func (k vendorKey) toAttemptKey() AttemptKey { return AttemptKey(k) }

// SendKey is the exported (vendor, status) tuple for counter lookups.
type SendKey struct {
	Vendor Vendor
	Status Status
}

// AttemptKey is the exported vendor key for attempt counter lookups.
type AttemptKey struct {
	Vendor Vendor
}

// NewRegistry returns a Registry backed by an OTel meter (which may be
// nil for tests). The meter is wrapped in a counter + histogram that the
// caller can scrape.
func NewRegistry(meter metric.Meter) *Registry {
	r := &Registry{
		counter:  make(map[counterKey]int64),
		durSum:   make(map[counterKey]time.Duration),
		attempts: make(map[vendorKey]int64),
		meter:    meter,
	}
	if meter != nil {
		// Best-effort: if the meter does not support these instruments
		// (eg. the noop meter in tests), ignore the error and continue.
		r.intCnt, _ = meter.Int64Counter("notify_send_total")
		r.durHst, _ = meter.Float64Histogram("notify_send_duration_seconds")
		r.attCnt, _ = meter.Int64Counter("notify_send_attempts_total")
	}
	return r
}

// IncSend records one send outcome.
func (r *Registry) IncSend(ctx context.Context, v Vendor, s Status) {
	k := counterKey{Vendor: v, Status: s}
	r.mu.Lock()
	r.counter[k]++
	r.mu.Unlock()
	if r.intCnt != nil {
		r.intCnt.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("vendor", string(v)),
				attribute.String("status", string(s)),
			),
		)
	}
}

// ObserveSend records a send duration.
func (r *Registry) ObserveSend(ctx context.Context, v Vendor, s Status, d time.Duration) {
	k := counterKey{Vendor: v, Status: s}
	r.mu.Lock()
	r.durSum[k] += d
	r.mu.Unlock()
	if r.durHst != nil {
		r.durHst.Record(ctx, d.Seconds(),
			metric.WithAttributes(
				attribute.String("vendor", string(v)),
				attribute.String("status", string(s)),
			),
		)
	}
}

// IncAttempt records one HTTP attempt (including retries).
func (r *Registry) IncAttempt(ctx context.Context, v Vendor) {
	k := vendorKey{Vendor: v}
	r.mu.Lock()
	r.attempts[k]++
	r.mu.Unlock()
	if r.attCnt != nil {
		r.attCnt.Add(ctx, 1,
			metric.WithAttributes(attribute.String("vendor", string(v))),
		)
	}
}

// Snapshot is a point-in-time view of the counters. Returned to operators
// via dashboard endpoints and to tests via the helpers below.
type Snapshot struct {
	SendCounts map[SendKey]int64         `json:"send_counts"`
	SendDur    map[SendKey]time.Duration `json:"send_duration_sum"`
	Attempts   map[AttemptKey]int64      `json:"attempts"`
}

// Snapshot returns the current counter values.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sc := make(map[SendKey]int64, len(r.counter))
	for k, v := range r.counter {
		sc[k.toSendKey()] = v
	}
	sd := make(map[SendKey]time.Duration, len(r.durSum))
	for k, v := range r.durSum {
		sd[k.toSendKey()] = v
	}
	at := make(map[AttemptKey]int64, len(r.attempts))
	for k, v := range r.attempts {
		at[k.toAttemptKey()] = v
	}
	return Snapshot{SendCounts: sc, SendDur: sd, Attempts: at}
}

// TotalByVendor returns the number of sends for a given vendor across
// all statuses. Used by the rotation-observability dashboard.
func (r *Registry) TotalByVendor(v Vendor) int64 {
	var n int64
	for k, c := range r.Snapshot().SendCounts {
		if k.Vendor == v {
			n += c
		}
	}
	return n
}

// StatusFor returns the count for a given (vendor, status) pair.
func (r *Registry) StatusFor(v Vendor, s Status) int64 {
	return r.Snapshot().SendCounts[SendKey{Vendor: v, Status: s}]
}

// Attempts returns the per-vendor attempt counter. v17409-6.
func (r *Registry) Attempts(v Vendor) int64 {
	return r.Snapshot().Attempts[AttemptKey{Vendor: v}]
}
