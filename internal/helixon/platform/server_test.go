package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/helixon/platform"
)

// TestPlatformServer_HealthAndMessages_v8900 pins the v8900-B6 contract:
// the platform HTTP server exposes /healthz (200) and POST /v1/messages
// that returns a JSON envelope with the runtime's response. Streaming via
// SSE is covered by TestPlatformServer_SSE_v8900.
func TestPlatformServer_HealthAndMessages_v8900(t *testing.T) {
	handler := func(_ context.Context, msg helixon.IncomingMessage) (string, error) {
		if msg.Channel != "platform" {
			t.Fatalf("expected channel=platform, got %q", msg.Channel)
		}
		return "echo:" + msg.Content, nil
	}

	srv := platform.NewServer(platform.Config{Addr: "127.0.0.1:0"}, handler)
	mux := srv.Routes()

	// /healthz
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz: expected 200, got %d", rec.Code)
	}
	var hb map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &hb); err != nil {
		t.Fatalf("/healthz body: %v", err)
	}
	if hb["status"] != "ok" {
		t.Fatalf("/healthz status: %q", hb["status"])
	}

	// POST /v1/messages
	body := bytes.NewBufferString(`{"content":"hello"}`)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/messages: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("/v1/messages body: %v", err)
	}
	if got["response"] != "echo:hello" {
		t.Fatalf("response: %v", got["response"])
	}
}

// TestPlatformServer_BadRequest_v8900 pins the input-validation contract.
func TestPlatformServer_BadRequest_v8900(t *testing.T) {
	handler := func(_ context.Context, _ helixon.IncomingMessage) (string, error) {
		t.Fatalf("handler must not be called for bad request")
		return "", nil
	}
	srv := platform.NewServer(platform.Config{Addr: "127.0.0.1:0"}, handler)
	mux := srv.Routes()

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"missing content", `{}`},
		{"blank content", `{"content":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestPlatformServer_SSE_v8900 pins the v8900-B7 contract: the SSE endpoint
// streams `data:` lines for each chunk emitted by the StreamHandler and
// terminates with `event: done`.
func TestPlatformServer_SSE_v8900(t *testing.T) {
	stream := func(ctx context.Context, _ helixon.IncomingMessage, emit func(chunk string) error) error { //nolint:revive // unused-parameter required by interface
		for _, c := range []string{"hello", " ", "world"} {
			if err := emit(c); err != nil {
				return err
			}
		}
		return nil
	}
	srv := platform.NewServer(platform.Config{
		Addr:          "127.0.0.1:0",
		StreamHandler: stream,
	}, func(_ context.Context, _ helixon.IncomingMessage) (string, error) {
		return "", nil
	})
	mux := srv.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/stream", strings.NewReader(`{"content":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sse: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("sse: Content-Type=%q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"data: hello\n", "data:  \n", "data: world\n", "event: done\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sse: missing %q in:\n%s", want, body)
		}
	}
}

// TestPlatformServer_Listen_v8900 verifies the server binds to a real port
// (driven by httptest.NewServer's Listen) and returns the bound addr.
func TestPlatformServer_Listen_v8900(t *testing.T) {
	handler := func(_ context.Context, _ helixon.IncomingMessage) (string, error) {
		return "ok", nil
	}
	srv := platform.NewServer(platform.Config{Addr: "127.0.0.1:0"}, handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	deadline := time.After(2 * time.Second)
	for srv.BoundAddr() == "" {
		select {
		case <-deadline:
			t.Fatal("server did not bind within 2s")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	resp, err := http.Get("http://" + srv.BoundAddr() + "/healthz")
	if err != nil {
		t.Fatalf("healthz GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Serve returned: %v", err)
	}
}

// --- v12022 streaming hardening tests ---

// TestPlatformServer_StreamContextCancellation_v12022 verifies the stream
// handler detects context cancellation (client disconnect) and stops emitting.
func TestPlatformServer_StreamContextCancellation_v12022(t *testing.T) {
	var emitCount atomic.Int32
	stream := func(ctx context.Context, _ helixon.IncomingMessage, emit func(string) error) error {
		for i := 0; i < 100; i++ {
			if err := emit("chunk"); err != nil {
				return err
			}
			emitCount.Add(1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		return nil
	}

	srv := platform.NewServer(platform.Config{
		Addr:          "127.0.0.1:0",
		StreamHandler: stream,
	}, func(_ context.Context, _ helixon.IncomingMessage) (string, error) {
		return "", nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	deadline := time.After(2 * time.Second)
	for srv.BoundAddr() == "" {
		select {
		case <-deadline:
			t.Fatal("server did not bind within 2s")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer reqCancel()

	req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"http://"+srv.BoundAddr()+"/v1/messages/stream",
		strings.NewReader(`{"content":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}

	time.Sleep(50 * time.Millisecond)
	if emitCount.Load() >= 100 {
		t.Fatalf("expected stream to abort early, got %d emits", emitCount.Load())
	}

	cancel()
	<-errCh
}

// TestPlatformServer_StreamHandlerError_v12022 verifies the error event
// is emitted with the handler's error message.
func TestPlatformServer_StreamHandlerError_v12022(t *testing.T) {
	stream := func(_ context.Context, _ helixon.IncomingMessage, _ func(string) error) error {
		return errors.New("generation failed: model overloaded")
	}
	srv := platform.NewServer(platform.Config{
		Addr:          "127.0.0.1:0",
		StreamHandler: stream,
	}, func(_ context.Context, _ helixon.IncomingMessage) (string, error) { return "", nil })
	mux := srv.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/stream", strings.NewReader(`{"content":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("expected error event, got:\n%s", body)
	}
	if !strings.Contains(body, "model overloaded") {
		t.Fatalf("expected error message in body, got:\n%s", body)
	}
	if strings.Contains(body, "event: done") {
		t.Fatalf("should not emit done after error")
	}
}

// TestPlatformServer_StreamNoConfigured_v12022 verifies 501 when StreamHandler is nil.
func TestPlatformServer_StreamNoConfigured_v12022(t *testing.T) {
	srv := platform.NewServer(platform.Config{Addr: "127.0.0.1:0"},
		func(_ context.Context, _ helixon.IncomingMessage) (string, error) { return "", nil })
	mux := srv.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/stream", strings.NewReader(`{"content":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

// TestPlatformServer_StreamHeartbeat_v12022 verifies the SSE heartbeat comment
// is emitted during long-running streams.
func TestPlatformServer_StreamHeartbeat_v12022(t *testing.T) {
	stream := func(ctx context.Context, _ helixon.IncomingMessage, emit func(string) error) error { //nolint:revive // unused-parameter required by interface
		time.Sleep(80 * time.Millisecond)
		return emit("final")
	}
	srv := platform.NewServer(platform.Config{
		Addr:              "127.0.0.1:0",
		HeartbeatInterval: 20 * time.Millisecond,
		StreamHandler:     stream,
	}, func(_ context.Context, _ helixon.IncomingMessage) (string, error) { return "", nil })
	mux := srv.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/stream", strings.NewReader(`{"content":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, ": heartbeat") {
		t.Fatalf("expected heartbeat comment in SSE output, got:\n%s", body)
	}
	if !strings.Contains(body, "data: final") {
		t.Fatalf("expected final chunk, got:\n%s", body)
	}
}

// TestPlatformServer_ConcurrentRequests_v12022 verifies the server handles
// multiple concurrent blocking requests without deadlocking.
func TestPlatformServer_ConcurrentRequests_v12022(t *testing.T) {
	var callCount atomic.Int32
	handler := func(_ context.Context, msg helixon.IncomingMessage) (string, error) {
		callCount.Add(1)
		time.Sleep(5 * time.Millisecond)
		return "reply:" + msg.Content, nil
	}
	srv := platform.NewServer(platform.Config{Addr: "127.0.0.1:0"}, handler)
	mux := srv.Routes()

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			body := bytes.NewBufferString(`{"content":"msg"}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("request %d: expected 200, got %d", idx, rec.Code)
			}
		}(i)
	}
	wg.Wait()

	if callCount.Load() != N {
		t.Fatalf("expected %d handler calls, got %d", N, callCount.Load())
	}
}

// TestPlatformServer_SessionPassthrough_v12022 verifies session_id
// is propagated through both blocking and streaming endpoints.
func TestPlatformServer_SessionPassthrough_v12022(t *testing.T) {
	handler := func(_ context.Context, msg helixon.IncomingMessage) (string, error) {
		return "session=" + msg.SessionID, nil
	}
	srv := platform.NewServer(platform.Config{Addr: "127.0.0.1:0"}, handler)
	mux := srv.Routes()

	body := bytes.NewBufferString(`{"session_id":"s123","content":"hi"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["session_id"] != "s123" {
		t.Fatalf("session_id not echoed: %v", resp)
	}
	if resp["response"] != "session=s123" {
		t.Fatalf("handler did not receive session_id: %v", resp)
	}
}

// TestPlatformServer_MessageHandlerError_v12022 verifies 500 on handler error.
func TestPlatformServer_MessageHandlerError_v12022(t *testing.T) {
	handler := func(_ context.Context, _ helixon.IncomingMessage) (string, error) {
		return "", errors.New("internal failure")
	}
	srv := platform.NewServer(platform.Config{Addr: "127.0.0.1:0"}, handler)
	mux := srv.Routes()

	body := bytes.NewBufferString(`{"content":"hi"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !strings.Contains(resp["error"], "internal failure") {
		t.Fatalf("error message not propagated: %v", resp)
	}
}

// TestPlatformServer_StreamNewlineEscape_v12022 verifies multi-line
// chunks are escaped correctly in SSE data lines.
func TestPlatformServer_StreamNewlineEscape_v12022(t *testing.T) {
	stream := func(_ context.Context, _ helixon.IncomingMessage, emit func(string) error) error {
		return emit("line1\nline2\nline3")
	}
	srv := platform.NewServer(platform.Config{
		Addr:          "127.0.0.1:0",
		StreamHandler: stream,
	}, func(_ context.Context, _ helixon.IncomingMessage) (string, error) { return "", nil })
	mux := srv.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/stream", strings.NewReader(`{"content":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `data: line1\nline2\nline3`) {
		t.Fatalf("newlines not escaped: %s", body)
	}
}
