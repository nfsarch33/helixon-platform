package svcregistry

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the Prometheus collector for the registry. It exposes a
// single counter labelled by (op, status). Each Metrics instance owns
// a private prometheus.Registry so multiple Registries can coexist
// without panicking on duplicate collector registration.
type Metrics struct {
	OpsTotal *prometheus.CounterVec
	PROMReg  *prometheus.Registry

	mu      sync.Mutex
	counter map[string]uint64 // keyed by op|status for tests + Prometheus scrape fallback
}

// newMetrics constructs a Metrics and registers OpsTotal with a fresh
// prometheus.Registry. Use NewSharedMetrics to share a registerer when
// wiring into the default Prometheus scrape pipeline.
func newMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	cv := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "svcregistry_operations_total",
			Help: "Total number of svcregistry operations partitioned by op and status.",
		},
		[]string{"op", "status"},
	)
	reg.MustRegister(cv)
	return &Metrics{
		OpsTotal: cv,
		PROMReg:  reg,
		counter:  make(map[string]uint64),
	}
}

// Inc increments the counter for (op, status) by one. Thread-safe.
func (m *Metrics) Inc(op, status string) {
	if m == nil {
		return
	}
	m.OpsTotal.WithLabelValues(op, status).Inc()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter[op+"|"+status]++
}

// Value returns the local counter snapshot for (op, status). Used by tests.
func (m *Metrics) Value(op, status string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counter[op+"|"+status]
}