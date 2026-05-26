package platform_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/platform"
)

// TestFromHandler_StreamFinalResponse_v8900 pins B12: when the platform
// server is built from a single MessageHandler, the streaming endpoint
// emits the full response as a single SSE chunk.
func TestFromHandler_StreamFinalResponse_v8900(t *testing.T) {
	handler := func(_ context.Context, msg helixon.IncomingMessage) (string, error) {
		return "final:" + msg.Content, nil
	}
	srv := platform.FromHandler(handler, platform.Config{Addr: "127.0.0.1:0"})
	mux := srv.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages/stream", strings.NewReader(`{"content":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "data: final:go\n") {
		t.Fatalf("missing final chunk: %s", body)
	}
	if !strings.Contains(body, "event: done\n") {
		t.Fatalf("missing done event: %s", body)
	}
}
