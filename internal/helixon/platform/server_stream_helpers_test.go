package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
)

// TestWriteSSEHeaders verifies the SSE response prelude.
func TestWriteSSEHeaders(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	writeSSEHeaders(w)
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: got %q want text/event-stream", got)
	}
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering: got %q want no", got)
	}
	if w.Code != 200 {
		t.Errorf("status: got %d want 200", w.Code)
	}
}

// TestSafeOneLine escapes newline characters so SSE events stay on a single line.
func TestSafeOneLine(t *testing.T) {
	t.Parallel()
	got := safeOneLine("first\nsecond\nthird")
	if got != "first\\nsecond\\nthird" {
		t.Errorf("got %q, want escaped newlines", got)
	}
	got = safeOneLine("clean")
	if got != "clean" {
		t.Errorf("got %q, want passthrough", got)
	}
}

// TestNewHeartbeatLoop runs the heartbeat for a short interval and confirms
// the SSE comment appears on the writer.
func TestNewHeartbeatLoop(t *testing.T) {
	t.Parallel()
	// Use raw buffer to inspect writes.
	var buf strings.Builder
	var mu sync.Mutex
	stop := newHeartbeatLoop(&mu, &buf, 5*time.Millisecond, func() {})
	defer stop()
	time.Sleep(25 * time.Millisecond)
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, ": heartbeat") {
		t.Errorf("heartbeat body missing: got %q", out)
	}
}

// TestNewEMIT_NoClientDisconnect tests the happy path; ctx not done.
func TestNewEMIT_NoClientDisconnect(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	var mu sync.Mutex
	ctx := context.Background()
	emit := newEmitFn(ctx, &mu, &buf)
	if err := emit("hello"); err != nil {
		t.Fatalf("emit: %v", err)
	}
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "data: hello") {
		t.Errorf("event missing: %q", out)
	}
	if !strings.Contains(out, "\n\n") {
		t.Errorf("SSE double-newline terminator missing")
	}
}

// TestNewEMIT_ClientDisconnect returns the ctx error.
func TestNewEMIT_ClientDisconnect(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf strings.Builder
	var mu sync.Mutex
	emit := newEmitFn(ctx, &mu, &buf)
	err := emit("ignored")
	if err == nil {
		t.Fatal("expected error for cancelled ctx")
	}
	if !errors.Is(err, context.Canceled) {
		// Our wrapper says "client disconnected: ..." so we test containment.
		if !strings.Contains(err.Error(), "client disconnected") {
			t.Errorf("error not wrapped as client disconnect: %v", err)
		}
	}
}

// newDiscardLogger returns a slog.Logger that drops all records. Used to
// keep test output clean.
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// import alias for slog in tests file
var _ = slog.LevelDebug

// TestHandleStream_NotConfigured writes 501 when StreamHandler is nil.
func TestHandleStream_NotConfigured(t *testing.T) {
	t.Parallel()
	s := &Server{cfg: Config{}, logger: newDiscardLogger()}
	req := httptest.NewRequest("POST", "/v1/messages/stream", strings.NewReader(`{"session_id":"s1","content":"hi"}`))
	w := httptest.NewRecorder()
	s.handleStream(w, req)
	if w.Code != 501 {
		t.Errorf("got %d, want 501", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "streaming not configured") {
		t.Errorf("body missing expected error: %q", body)
	}
}

// TestHandleStream_NoFlusher writes 500 when transport does not support Flusher.
func TestHandleStream_NoFlusher(t *testing.T) {
	t.Parallel()
	s := &Server{cfg: Config{StreamHandler: func(ctx context.Context, m helixon.IncomingMessage, emit func(string) error) error { //nolint:revive // unused-parameter required by interface
		return nil
	}}, logger: newDiscardLogger()}
	req := httptest.NewRequest("POST", "/v1/messages/stream",
		strings.NewReader(`{"session_id":"s1","content":"hi"}`))
	w := &nonFlusherRecorder{w: httptest.NewRecorder()}
	s.handleStream(w, req)
	if w.w.Code != 500 {
		t.Errorf("got %d, want 500", w.w.Code)
	}
}

// TestHandleStream_HappyPath drives a successful handler and asserts the
// terminal event: done / data: [DONE] is written.
func TestHandleStream_HappyPath(t *testing.T) {
	t.Parallel()
	s := &Server{cfg: Config{
		StreamHandler: func(ctx context.Context, m helixon.IncomingMessage, emit func(string) error) error { //nolint:revive // unused-parameter required by interface
			if err := emit("hello"); err != nil {
				return err
			}
			return nil
		},
		HeartbeatInterval: 100 * time.Millisecond,
	}, logger: newDiscardLogger()}
	req := httptest.NewRequest("POST", "/v1/messages/stream",
		strings.NewReader(`{"session_id":"s1","content":"hi"}`))
	w := httptest.NewRecorder()
	s.handleStream(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "data: hello") {
		t.Errorf("handler chunk missing: %q", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("terminal done event missing: %q", body)
	}
}

// TestHandleStream_HandlerError emits event: error when handler returns err.
func TestHandleStream_HandlerError(t *testing.T) {
	t.Parallel()
	s := &Server{cfg: Config{
		StreamHandler: func(ctx context.Context, m helixon.IncomingMessage, emit func(string) error) error { //nolint:revive // unused-parameter required by interface
			return errors.New("bad provider")
		},
		HeartbeatInterval: 100 * time.Millisecond,
	}, logger: newDiscardLogger()}
	req := httptest.NewRequest("POST", "/v1/messages/stream",
		strings.NewReader(`{"session_id":"s1","content":"hi"}`))
	w := httptest.NewRecorder()
	s.handleStream(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("error event missing: %q", body)
	}
	if !strings.Contains(body, "bad provider") {
		t.Errorf("provider message missing: %q", body)
	}
}

// TestEncodeMessage ensures the wire shape matches what we decode.
func TestEncodeMessage(t *testing.T) {
	t.Parallel()
	msg := helixon.IncomingMessage{SessionID: "sX", Channel: "platform-stream", Content: "hi"}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"session_id":"sX"`) {
		t.Errorf("encoded shape drift: %s", b)
	}
}

// nonFlusherRecorder hides httptest.ResponseRecorder's Flusher capability to
// exercise the !ok branch in handleStream. Embedding httptest.ResponseRecorder
// would still satisfy http.Flusher, so we expose only the http.ResponseWriter
// surface (Write + Header + WriteHeader).
type nonFlusherRecorder struct {
	w *httptest.ResponseRecorder
}

func (n *nonFlusherRecorder) Header() http.Header         { return n.w.Header() }
func (n *nonFlusherRecorder) Write(p []byte) (int, error) { return n.w.Write(p) }
func (n *nonFlusherRecorder) WriteHeader(code int)        { n.w.WriteHeader(code) }
