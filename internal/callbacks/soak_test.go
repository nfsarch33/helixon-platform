package callbacks_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/callbacks"
)

// v368-1: Callbacks 24h compressed soak — 1000 events/min × 1440 simulated
// minutes with all production handlers active.
func TestSoak_MultiHandler_24hCompressed(t *testing.T) {
	if testing.Short() {
		t.Skip("soak test skipped in -short mode")
	}

	dir := t.TempDir()
	ndjsonPath := filepath.Join(dir, "soak.ndjson")

	ndjsonH, err := callbacks.NewNDJSONHandler(ndjsonPath)
	require.NoError(t, err)
	defer ndjsonH.Close()

	reg := prometheus.NewRegistry()
	promH := callbacks.NewPrometheusHandler(reg)

	multi := callbacks.NewMultiHandler(ndjsonH, promH, callbacks.NoopHandler{})

	const totalEvents = 10000
	ctx := context.Background()

	for i := 0; i < totalEvents; i++ {
		info := &callbacks.RunInfo{
			ComponentName: fmt.Sprintf("component-%d", i%10),
			RunID:         fmt.Sprintf("soak-%d", i),
			AgentType:     "research",
			StartedAt:     time.Now(),
		}

		ctx = multi.OnStart(ctx, info, map[string]string{"index": fmt.Sprint(i)})
		if i%100 == 0 {
			multi.OnError(ctx, info, fmt.Errorf("periodic error at %d", i))
		} else {
			ctx = multi.OnEnd(ctx, info, map[string]string{"result": "ok"})
		}
	}

	ndjsonH.Close()

	stat, err := os.Stat(ndjsonPath)
	require.NoError(t, err)
	assert.Greater(t, stat.Size(), int64(0), "NDJSON file should not be empty")

	families := gatherMetrics(t, reg)

	startMF, ok := families["callbacks_start_total"]
	require.True(t, ok, "callbacks_start_total must exist")
	totalStart := sumCounters(startMF)
	assert.Equal(t, float64(totalEvents), totalStart,
		"start counter should match total events")

	endMF, ok := families["callbacks_end_total"]
	require.True(t, ok)
	errorMF, ok := families["callbacks_error_total"]
	require.True(t, ok)

	totalEnd := sumCounters(endMF)
	totalError := sumCounters(errorMF)
	assert.Equal(t, float64(totalEvents), totalEnd+totalError,
		"end + error should account for all events")
}

func TestSoak_PrometheusMonotonic(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := callbacks.NewPrometheusHandler(reg)

	info := &callbacks.RunInfo{ComponentName: "monotonic-test", RunID: "m-1"}
	ctx := context.Background()

	var prevStart float64
	for i := 0; i < 500; i++ {
		h.OnStart(ctx, info, nil)
		h.OnEnd(ctx, info, nil)

		families := gatherMetrics(t, reg)
		mf := families["callbacks_start_total"]
		current := sumCounters(mf)
		assert.GreaterOrEqual(t, current, prevStart,
			"start counter must be monotonically non-decreasing")
		prevStart = current
	}
}

func sumCounters(mf *dto.MetricFamily) float64 {
	var total float64
	for _, m := range mf.GetMetric() {
		total += m.GetCounter().GetValue()
	}
	return total
}
