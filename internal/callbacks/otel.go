package callbacks

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type spanKey struct{}

// OTELHandler creates OpenTelemetry spans for each callback lifecycle event.
// OnStart begins a new span as a child of any existing span in the context.
// OnEnd/OnError end the span with appropriate status.
type OTELHandler struct {
	tracer trace.Tracer
}

// NewOTELHandler creates a handler using a named tracer from the global provider.
func NewOTELHandler(tracerName string) *OTELHandler {
	return &OTELHandler{
		tracer: otel.Tracer(tracerName),
	}
}

// NewOTELHandlerWithTracer creates a handler with an explicit tracer,
// useful for testing with a noop or recording tracer provider.
func NewOTELHandlerWithTracer(tracer trace.Tracer) *OTELHandler {
	return &OTELHandler{tracer: tracer}
}

func (h *OTELHandler) OnStart(ctx context.Context, info *RunInfo, input any) context.Context { //nolint:revive // unused-parameter required by interface
	attrs := []attribute.KeyValue{
		attribute.String("component.name", info.ComponentName),
		attribute.String("run.id", info.RunID),
	}
	if info.AgentType != "" {
		attrs = append(attrs, attribute.String("agent.type", info.AgentType))
	}
	if chain := info.ParentChain(); len(chain) > 1 {
		attrs = append(attrs, attribute.StringSlice("parent.chain", chain[:len(chain)-1]))
	}
	for k, v := range info.Tags {
		attrs = append(attrs, attribute.String("tag."+k, v))
	}

	ctx, span := h.tracer.Start(ctx, info.ComponentName,
		trace.WithAttributes(attrs...),
		trace.WithSpanKind(trace.SpanKindInternal),
	)

	return context.WithValue(ctx, spanKey{}, span)
}

func (h *OTELHandler) OnEnd(ctx context.Context, info *RunInfo, _ any) context.Context { //nolint:revive // unused-parameter required by interface
	if span, ok := ctx.Value(spanKey{}).(trace.Span); ok && span.IsRecording() {
		span.SetStatus(codes.Ok, "")
		span.End()
	}
	return ctx
}

func (h *OTELHandler) OnError(ctx context.Context, info *RunInfo, err error) context.Context {
	if span, ok := ctx.Value(spanKey{}).(trace.Span); ok && span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, fmt.Sprintf("%s failed: %v", info.ComponentName, err))
		span.End()
	}
	return ctx
}
