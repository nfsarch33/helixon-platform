package callbacks

import (
	"context"
	"time"
)

// RunInfo carries per-invocation metadata through the callback chain.
// Parent links form a tree that mirrors nested component calls.
type RunInfo struct {
	ComponentName string
	RunID         string
	AgentType     string
	StartedAt     time.Time
	Parent        *RunInfo
	Tags          map[string]string
	Attrs         map[string]any
}

// ParentChain returns the component names from root to this node (inclusive),
// useful for trace breadcrumbs and OTEL span hierarchies.
func (r *RunInfo) ParentChain() []string {
	if r == nil {
		return nil
	}
	chain := r.Parent.ParentChain()
	return append(chain, r.ComponentName)
}

// Handler receives lifecycle events from Runnable executions. Each method
// returns a (possibly enriched) context that downstream code should use.
type Handler interface {
	OnStart(ctx context.Context, info *RunInfo, input any) context.Context
	OnEnd(ctx context.Context, info *RunInfo, output any) context.Context
	OnError(ctx context.Context, info *RunInfo, err error) context.Context
}

type contextKey struct{}
type handlerKey struct{}

// WithRunInfo stores a RunInfo in the context.
func WithRunInfo(ctx context.Context, info *RunInfo) context.Context {
	return context.WithValue(ctx, contextKey{}, info)
}

// RunInfoFromContext retrieves the RunInfo from the context, or nil.
func RunInfoFromContext(ctx context.Context) *RunInfo {
	v, _ := ctx.Value(contextKey{}).(*RunInfo)
	return v
}

// WithHandler stores a Handler in the context so that nested Runnable
// invocations can emit callbacks without threading the handler explicitly.
func WithHandler(ctx context.Context, h Handler) context.Context {
	return context.WithValue(ctx, handlerKey{}, h)
}

// HandlerFromContext retrieves the Handler from the context, or nil.
func HandlerFromContext(ctx context.Context) Handler {
	v, _ := ctx.Value(handlerKey{}).(Handler)
	return v
}
