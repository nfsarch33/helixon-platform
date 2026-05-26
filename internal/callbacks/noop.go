package callbacks

import "context"

// NoopHandler is a Handler that does nothing. Useful as a default or in tests.
type NoopHandler struct{}

func (NoopHandler) OnStart(ctx context.Context, _ *RunInfo, _ any) context.Context   { return ctx }
func (NoopHandler) OnEnd(ctx context.Context, _ *RunInfo, _ any) context.Context     { return ctx }
func (NoopHandler) OnError(ctx context.Context, _ *RunInfo, _ error) context.Context { return ctx }
