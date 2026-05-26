package callbacks

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type startTimeKey struct{}

// PrometheusHandler emits Prometheus counters and a duration histogram
// for each callback lifecycle event, labeled by component_name.
type PrometheusHandler struct {
	startTotal *prometheus.CounterVec
	endTotal   *prometheus.CounterVec
	errorTotal *prometheus.CounterVec
	duration   *prometheus.HistogramVec
}

// NewPrometheusHandler registers callbacks_* metrics with the given registerer.
func NewPrometheusHandler(reg prometheus.Registerer) *PrometheusHandler {
	labels := []string{"component_name"}

	h := &PrometheusHandler{
		startTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "callbacks_start_total",
			Help: "Total OnStart callbacks by component.",
		}, labels),
		endTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "callbacks_end_total",
			Help: "Total OnEnd callbacks by component.",
		}, labels),
		errorTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "callbacks_error_total",
			Help: "Total OnError callbacks by component.",
		}, labels),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "callbacks_duration_seconds",
			Help:    "Duration from OnStart to OnEnd by component.",
			Buckets: prometheus.DefBuckets,
		}, labels),
	}

	reg.MustRegister(h.startTotal, h.endTotal, h.errorTotal, h.duration)
	return h
}

func (h *PrometheusHandler) OnStart(ctx context.Context, info *RunInfo, _ any) context.Context {
	h.startTotal.WithLabelValues(info.ComponentName).Inc()
	return context.WithValue(ctx, startTimeKey{}, time.Now())
}

func (h *PrometheusHandler) OnEnd(ctx context.Context, info *RunInfo, _ any) context.Context {
	h.endTotal.WithLabelValues(info.ComponentName).Inc()
	if start, ok := ctx.Value(startTimeKey{}).(time.Time); ok {
		h.duration.WithLabelValues(info.ComponentName).Observe(time.Since(start).Seconds())
	}
	return ctx
}

func (h *PrometheusHandler) OnError(ctx context.Context, info *RunInfo, _ error) context.Context {
	h.errorTotal.WithLabelValues(info.ComponentName).Inc()
	return ctx
}
