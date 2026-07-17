package callbacks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/nfsarch33/helixon-platform/internal/callbacks"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ---------------------------------------------------------------------------
// RunInfo + context propagation
// ---------------------------------------------------------------------------

func TestRunInfo_ParentChain(t *testing.T) {
	root := &callbacks.RunInfo{
		ComponentName: "root",
		RunID:         "r-1",
		Tags:          map[string]string{"env": "test"},
	}
	child := &callbacks.RunInfo{
		ComponentName: "child",
		RunID:         "r-2",
		Parent:        root,
	}
	grandchild := &callbacks.RunInfo{
		ComponentName: "grandchild",
		RunID:         "r-3",
		Parent:        child,
	}

	assert.Nil(t, root.Parent)
	assert.Equal(t, "root", child.Parent.ComponentName)
	assert.Equal(t, "child", grandchild.Parent.ComponentName)
	assert.Equal(t, "root", grandchild.Parent.Parent.ComponentName)
}

func TestRunInfo_ParentChainMethod(t *testing.T) {
	root := &callbacks.RunInfo{ComponentName: "pipeline", RunID: "r-1"}
	step1 := &callbacks.RunInfo{ComponentName: "scrape", RunID: "r-1", Parent: root}
	step2 := &callbacks.RunInfo{ComponentName: "extract", RunID: "r-1", Parent: step1}

	assert.Equal(t, []string{"pipeline"}, root.ParentChain())
	assert.Equal(t, []string{"pipeline", "scrape"}, step1.ParentChain())
	assert.Equal(t, []string{"pipeline", "scrape", "extract"}, step2.ParentChain())
}

func TestRunInfo_ParentChainMethod_Nil(t *testing.T) {
	var info *callbacks.RunInfo
	assert.Nil(t, info.ParentChain())
}

func TestRunInfo_AgentTypeAndStartedAt(t *testing.T) {
	now := time.Now()
	info := &callbacks.RunInfo{
		ComponentName: "test",
		RunID:         "r-agent",
		AgentType:     "research",
		StartedAt:     now,
		Attrs:         map[string]any{"depth": 3},
	}
	assert.Equal(t, "research", info.AgentType)
	assert.Equal(t, now, info.StartedAt)
	assert.Equal(t, 3, info.Attrs["depth"])
}

func TestHandlerFromContext(t *testing.T) {
	h := callbacks.NoopHandler{}
	ctx := callbacks.WithHandler(context.Background(), h)
	got := callbacks.HandlerFromContext(ctx)
	assert.NotNil(t, got)
}

func TestHandlerFromContext_NilWhenMissing(t *testing.T) {
	got := callbacks.HandlerFromContext(context.Background())
	assert.Nil(t, got)
}

func TestContextPropagation(t *testing.T) {
	info := &callbacks.RunInfo{
		ComponentName: "test-component",
		RunID:         "ctx-1",
	}

	ctx := callbacks.WithRunInfo(context.Background(), info)
	got := callbacks.RunInfoFromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "test-component", got.ComponentName)
	assert.Equal(t, "ctx-1", got.RunID)
}

func TestContextPropagation_NilWhenMissing(t *testing.T) {
	got := callbacks.RunInfoFromContext(context.Background())
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// NoopHandler
// ---------------------------------------------------------------------------

func TestNoopHandler_NoSideEffects(t *testing.T) {
	h := callbacks.NoopHandler{}
	info := &callbacks.RunInfo{ComponentName: "noop-test", RunID: "n-1"}
	ctx := context.Background()

	ctx2 := h.OnStart(ctx, info, "input")
	assert.Equal(t, ctx, ctx2)

	ctx3 := h.OnEnd(ctx, info, "output")
	assert.Equal(t, ctx, ctx3)

	ctx4 := h.OnError(ctx, info, errors.New("boom"))
	assert.Equal(t, ctx, ctx4)
}

// ---------------------------------------------------------------------------
// NDJSONHandler
// ---------------------------------------------------------------------------

func TestNDJSONHandler_WritesEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.ndjson")

	h, err := callbacks.NewNDJSONHandler(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()

	info := &callbacks.RunInfo{ComponentName: "llm-call", RunID: "nd-1"}
	ctx := context.Background()

	h.OnStart(ctx, info, map[string]string{"prompt": "hello"})
	h.OnEnd(ctx, info, map[string]string{"response": "world"})

	_ = h.Close()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)

	var startEvt map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &startEvt))
	assert.Equal(t, "start", startEvt["event"])
	assert.Equal(t, "llm-call", startEvt["component_name"])
	assert.Equal(t, "nd-1", startEvt["run_id"])
	assert.NotEmpty(t, startEvt["timestamp"])

	var endEvt map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &endEvt))
	assert.Equal(t, "end", endEvt["event"])
	assert.Equal(t, "llm-call", endEvt["component_name"])
}

func TestNDJSONHandler_OnError_IncludesErrorMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.ndjson")

	h, err := callbacks.NewNDJSONHandler(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()

	info := &callbacks.RunInfo{ComponentName: "parser", RunID: "nd-2"}
	h.OnError(context.Background(), info, errors.New("parse failed: unexpected token"))

	_ = h.Close()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var evt map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(data), &evt))
	assert.Equal(t, "error", evt["event"])
	assert.Equal(t, "parse failed: unexpected token", evt["error"])
	assert.Equal(t, "parser", evt["component_name"])
}

// ---------------------------------------------------------------------------
// PrometheusHandler
// ---------------------------------------------------------------------------

func TestPrometheusHandler_IncrementsCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := callbacks.NewPrometheusHandler(reg)

	info := &callbacks.RunInfo{ComponentName: "embedder", RunID: "p-1"}
	ctx := context.Background()

	h.OnStart(ctx, info, nil)
	h.OnEnd(ctx, info, nil)
	h.OnError(ctx, info, errors.New("timeout"))

	families := gatherMetrics(t, reg)

	assertCounterValue(t, families, "callbacks_start_total", "embedder", 1)
	assertCounterValue(t, families, "callbacks_end_total", "embedder", 1)
	assertCounterValue(t, families, "callbacks_error_total", "embedder", 1)
}

func TestPrometheusHandler_RecordsDuration(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := callbacks.NewPrometheusHandler(reg)

	info := &callbacks.RunInfo{ComponentName: "retriever", RunID: "p-2"}
	ctx := context.Background()

	ctx = h.OnStart(ctx, info, nil)
	time.Sleep(10 * time.Millisecond)
	h.OnEnd(ctx, info, nil)

	families := gatherMetrics(t, reg)

	mf, ok := families["callbacks_duration_seconds"]
	require.True(t, ok, "callbacks_duration_seconds metric must exist")
	require.NotEmpty(t, mf.GetMetric())

	hist := mf.GetMetric()[0].GetHistogram()
	require.NotNil(t, hist)
	assert.Greater(t, hist.GetSampleSum(), 0.005,
		"duration should be at least 5ms")
	assert.Equal(t, uint64(1), hist.GetSampleCount())
}

// ---------------------------------------------------------------------------
// MultiHandler
// ---------------------------------------------------------------------------

func TestMultiHandler_FansOutToAll(t *testing.T) {
	var calls []string
	spy1 := &spyHandler{name: "spy1", calls: &calls}
	spy2 := &spyHandler{name: "spy2", calls: &calls}

	multi := callbacks.NewMultiHandler(spy1, spy2)
	info := &callbacks.RunInfo{ComponentName: "chain", RunID: "m-1"}
	ctx := context.Background()

	multi.OnStart(ctx, info, "in")
	multi.OnEnd(ctx, info, "out")
	multi.OnError(ctx, info, errors.New("err"))

	assert.Equal(t, []string{
		"spy1:start", "spy2:start",
		"spy1:end", "spy2:end",
		"spy1:error", "spy2:error",
	}, calls)
}

func TestMultiHandler_ContinuesOnHandlerError(t *testing.T) {
	var calls []string
	panicky := &panickingHandler{}
	spy := &spyHandler{name: "spy", calls: &calls}

	multi := callbacks.NewMultiHandler(panicky, spy)
	info := &callbacks.RunInfo{ComponentName: "chain", RunID: "m-2"}
	ctx := context.Background()

	assert.NotPanics(t, func() {
		multi.OnStart(ctx, info, nil)
		multi.OnEnd(ctx, info, nil)
		multi.OnError(ctx, info, errors.New("err"))
	})

	assert.Equal(t, []string{"spy:start", "spy:end", "spy:error"}, calls)
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

type spyHandler struct {
	name  string
	calls *[]string
}

func (s *spyHandler) OnStart(_ context.Context, _ *callbacks.RunInfo, _ any) context.Context {
	*s.calls = append(*s.calls, s.name+":start")
	return context.Background()
}

func (s *spyHandler) OnEnd(_ context.Context, _ *callbacks.RunInfo, _ any) context.Context {
	*s.calls = append(*s.calls, s.name+":end")
	return context.Background()
}

func (s *spyHandler) OnError(_ context.Context, _ *callbacks.RunInfo, _ error) context.Context {
	*s.calls = append(*s.calls, s.name+":error")
	return context.Background()
}

type panickingHandler struct{}

func (p *panickingHandler) OnStart(context.Context, *callbacks.RunInfo, any) context.Context {
	panic("boom in OnStart")
}

func (p *panickingHandler) OnEnd(context.Context, *callbacks.RunInfo, any) context.Context {
	panic("boom in OnEnd")
}

func (p *panickingHandler) OnError(context.Context, *callbacks.RunInfo, error) context.Context {
	panic("boom in OnError")
}

// ---------------------------------------------------------------------------
// Prometheus test helpers
// ---------------------------------------------------------------------------

func gatherMetrics(t *testing.T, reg *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	m := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		m[f.GetName()] = f
	}
	return m
}

func assertCounterValue(t *testing.T, families map[string]*dto.MetricFamily, name, component string, want float64) {
	t.Helper()
	mf, ok := families[name]
	require.True(t, ok, "metric %s must exist", name)
	for _, m := range mf.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "component_name" && lp.GetValue() == component {
				assert.Equal(t, want, m.GetCounter().GetValue(),
					"%s{component_name=%q}", name, component)
				return
			}
		}
	}
	t.Errorf("metric %s with component_name=%q not found", name, component)
}
