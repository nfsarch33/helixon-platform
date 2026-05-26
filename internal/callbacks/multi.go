package callbacks

import (
	"context"
	"fmt"
	"log/slog"
)

// MultiHandler fans out each callback to all wrapped handlers. If any
// handler panics, the panic is recovered and the remaining handlers
// still execute.
type MultiHandler struct {
	handlers []Handler
}

// NewMultiHandler creates a handler that delegates to all provided handlers.
func NewMultiHandler(handlers ...Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

func (m *MultiHandler) OnStart(ctx context.Context, info *RunInfo, input any) context.Context {
	for _, h := range m.handlers {
		ctx = safeCall(func() context.Context { return h.OnStart(ctx, info, input) }, ctx, "OnStart", info)
	}
	return ctx
}

func (m *MultiHandler) OnEnd(ctx context.Context, info *RunInfo, output any) context.Context {
	for _, h := range m.handlers {
		ctx = safeCall(func() context.Context { return h.OnEnd(ctx, info, output) }, ctx, "OnEnd", info)
	}
	return ctx
}

func (m *MultiHandler) OnError(ctx context.Context, info *RunInfo, err error) context.Context {
	for _, h := range m.handlers {
		ctx = safeCall(func() context.Context { return h.OnError(ctx, info, err) }, ctx, "OnError", info)
	}
	return ctx
}

func safeCall(fn func() context.Context, fallback context.Context, method string, info *RunInfo) (ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("callback handler panicked",
				"method", method,
				"component", info.ComponentName,
				"panic", fmt.Sprint(r),
			)
			ctx = fallback
		}
	}()
	return fn()
}
