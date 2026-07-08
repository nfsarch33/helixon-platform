package channel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubAgentRunner is the minimal AgentRunner used to verify errcheck fixes
// in the webhook + REPL handlers without requiring a live agent.
type stubAgentRunner struct {
	createSession func(ctx context.Context, agentID string) (string, error)
	run           func(ctx context.Context, sessionID, message string) (string, error)
}

func (s *stubAgentRunner) CreateSession(ctx context.Context, agentID string) (string, error) {
	if s.createSession != nil {
		return s.createSession(ctx, agentID)
	}
	return "stub-session", nil
}

func (s *stubAgentRunner) Run(ctx context.Context, sessionID, message string) (string, error) {
	if s.run != nil {
		return s.run(ctx, sessionID, message)
	}
	return "ok: " + message, nil
}

// TestWebhookRouter_HealthReturnsOK verifies CARRY-2026-0707-004 errcheck fix
// in the `/health` endpoint. Pre-fix fmt.Fprint returned a non-checked error;
// post-fix `_, _ = fmt.Fprint` silences errcheck without surfacing test noise.
func TestWebhookRouter_HealthReturnsOK(t *testing.T) {
	t.Parallel()

	agent := &stubAgentRunner{}
	handler := NewWebhookHandler(agent, WebhookConfig{
		AgentID: "test-agent",
	})
	router := Router(handler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected json content-type, got %q", ct)
	}
}

// TestREPL_RunWritesResponseWithSilencedErrcheck verifies CARRY-2026-0707-004
// errcheck fix in REPL.Run: 5 fmt.Fprintf/Fprint/Fprintln calls now use
// `_, _ = ...` instead of bare calls. The output should still contain the
// expected session header + prompt + response content + elapsed time.
func TestREPL_RunWritesResponseWithSilencedErrcheck(t *testing.T) {
	t.Parallel()

	agent := &stubAgentRunner{}
	repl := NewREPL(agent, REPLConfig{
		Prompt:  "test> ",
		AgentID: "test-cli",
	})

	input := "hello world\n"

	// Pipe: writer side feeds REPL; reader side collects output.
	// In the REPL's view, its `out` writes to the pipe and its `in` reads input.
	outR, outW := io.Pipe()

	// The REPL writes responses to its `out` parameter (which is the writer side).
	// We give REPL the writer for output, and feed it input directly.
	go func() {
		_ = repl.Run(context.Background(), strings.NewReader(input), outW)
		_ = outW.Close()
	}()

	got := readAll(t, outR, 4096)

	mustContain := []string{
		"Session: stub-session",
		"Type /exit to quit",
		"test>",
		"ok: hello world",
		"[",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\ngot:\n%s", want, got)
		}
	}
}

func readAll(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	total, _ := io.ReadFull(r, buf)
	return string(buf[:total])
}
