package platform

import (
	"context"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
)

// FromHandler builds a Server whose blocking and streaming endpoints
// both delegate to the same MessageHandler. Streaming is currently a thin
// shim that emits the final response in one chunk; a future ticket can
// swap StreamHandler for a token-by-token emitter once the agent loop
// exposes a per-token callback.
func FromHandler(handler helixon.MessageHandler, cfg Config) *Server {
	cfg.StreamHandler = func(ctx context.Context, msg helixon.IncomingMessage, emit func(string) error) error {
		out, err := handler(ctx, msg)
		if err != nil {
			return err
		}
		return emit(out)
	}
	return NewServer(cfg, handler)
}
