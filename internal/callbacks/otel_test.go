package callbacks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nfsarch33/helixon-platform/internal/callbacks"
)

func TestOTELHandler_OnStart_CreatesSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")
	h := callbacks.NewOTELHandlerWithTracer(tracer)

	info := &callbacks.RunInfo{
		ComponentName: "embedder",
		RunID:         "otel-1",
		AgentType:     "research",
		Tags:          map[string]string{"env": "test"},
	}

	ctx := h.OnStart(context.Background(), info, nil)
	h.OnEnd(ctx, info, nil)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "embedder", spans[0].Name)

	attrMap := spanAttrMap(spans[0])
	assert.Equal(t, "embedder", attrMap["component.name"])
	assert.Equal(t, "otel-1", attrMap["run.id"])
	assert.Equal(t, "research", attrMap["agent.type"])
	assert.Equal(t, "test", attrMap["tag.env"])
}

func TestOTELHandler_OnError_RecordsError(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")
	h := callbacks.NewOTELHandlerWithTracer(tracer)

	info := &callbacks.RunInfo{ComponentName: "parser", RunID: "otel-2"}

	ctx := h.OnStart(context.Background(), info, nil)
	h.OnError(ctx, info, errors.New("parse failed"))

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Len(t, spans[0].Events, 1, "error event should be recorded")
}

func TestOTELHandler_ParentChain_InAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())

	root := &callbacks.RunInfo{ComponentName: "root", RunID: "otel-3"}
	child := &callbacks.RunInfo{ComponentName: "child", RunID: "otel-3", Parent: root}

	tracer := tp.Tracer("test")
	h := callbacks.NewOTELHandlerWithTracer(tracer)

	ctx := h.OnStart(context.Background(), child, nil)
	h.OnEnd(ctx, child, nil)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	attrMap := spanAttrMap(spans[0])
	_, hasChain := attrMap["parent.chain"]
	assert.True(t, hasChain, "parent.chain attribute should be present for nested components")
}

func TestOTELHandler_OnEnd_WithoutStart_NoOp(t *testing.T) {
	h := callbacks.NewOTELHandler("test-noop")
	info := &callbacks.RunInfo{ComponentName: "orphan", RunID: "otel-4"}

	assert.NotPanics(t, func() {
		h.OnEnd(context.Background(), info, nil)
	})
}

func spanAttrMap(span tracetest.SpanStub) map[string]any {
	m := make(map[string]any)
	for _, attr := range span.Attributes {
		m[string(attr.Key)] = attr.Value.Emit()
	}
	return m
}
