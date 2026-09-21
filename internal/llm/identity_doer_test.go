package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// recordingDoer captures the outbound request after the decorator ran.
type recordingDoer struct {
	last *http.Request
	body []byte
}

func (r *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	r.last = req
	if req.Body != nil {
		r.body, _ = io.ReadAll(req.Body)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(http.NoBody)}, nil
}

// TestIdentityDoer_StampsHeadersFromCtx: every field of RunInfo becomes a
// header so the router can attribute traffic without sniffing bodies.
func TestIdentityDoer_StampsHeadersFromCtx(t *testing.T) {
	rec := &recordingDoer{}
	d := NewIdentityDoer(rec)
	ctx := WithRunInfo(context.Background(), RunInfo{
		RunID: "run-1", TicketID: "T-9", AgentID: "agent-x",
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream/v1", nil)
	if _, err := d.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	for header, want := range map[string]string{
		"X-HLXN-Run-Id":    "run-1",
		"X-HLXN-Ticket-Id": "T-9",
		"X-Helixon-Agent":  "agent-x",
	} {
		if got := rec.last.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// TestIdentityDoer_NoRunInfo_NoHeaders is the control: ordinary requests
// pass through untouched, so non-agent callers of the same client add no
// attribution noise.
func TestIdentityDoer_NoRunInfo_NoHeaders(t *testing.T) {
	rec := &recordingDoer{}
	d := NewIdentityDoer(rec)
	req, _ := http.NewRequest(http.MethodPost, "http://upstream/v1", nil)
	if _, err := d.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	for _, header := range []string{"X-HLXN-Run-Id", "X-HLXN-Ticket-Id", "X-Helixon-Agent"} {
		if got := rec.last.Header.Get(header); got != "" {
			t.Errorf("%s = %q, want empty", header, got)
		}
	}
}

// TestIdentityDoer_EmptyFieldsOmitted: a non-ticket run has no ticket id;
// the header must be absent, not empty, so router label cardinality stays
// bounded by real values.
func TestIdentityDoer_EmptyFieldsOmitted(t *testing.T) {
	rec := &recordingDoer{}
	d := NewIdentityDoer(rec)
	ctx := WithRunInfo(context.Background(), RunInfo{RunID: "run-2", AgentID: "agent-x"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream/v1", nil)
	_, _ = d.Do(req)
	if _, ok := rec.last.Header[http.CanonicalHeaderKey("X-HLXN-Ticket-Id")]; ok {
		t.Error("X-HLXN-Ticket-Id present for a run without a ticket")
	}
}

// TestIdentityDoer_ForwardsUntouched: URL, method and body are the inner
// request's - the decorator only adds headers, never rewrites traffic.
func TestIdentityDoer_ForwardsUntouched(t *testing.T) {
	rec := &recordingDoer{}
	d := NewIdentityDoer(rec)
	req, _ := http.NewRequest(http.MethodPost, "http://upstream/v1/chat", http.NoBody)
	req.Header.Set("Authorization", "Bearer x")
	if _, err := d.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if rec.last.URL.String() != "http://upstream/v1/chat" || rec.last.Method != http.MethodPost {
		t.Errorf("request rewritten: %s %s", rec.last.Method, rec.last.URL)
	}
	if rec.last.Header.Get("Authorization") != "Bearer x" {
		t.Error("existing headers lost")
	}
}

// TestRunInfoContextRoundTrip: values survive nesting under an ordinary
// derived context (timeout, cancel), which is how execute() wraps them.
func TestRunInfoContextRoundTrip(t *testing.T) {
	ctx := WithRunInfo(context.Background(), RunInfo{RunID: "r", AgentID: "a"})
	derived, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	info, ok := RunInfoFromContext(derived)
	if !ok || info.RunID != "r" || info.AgentID != "a" {
		t.Fatalf("info = %+v ok=%v", info, ok)
	}
	if _, ok := RunInfoFromContext(context.Background()); ok {
		t.Error("background ctx claims run info")
	}
}

var _ = httptest.NewServer // keep import stable if fixtures shrink
