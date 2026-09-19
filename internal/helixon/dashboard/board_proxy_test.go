package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// v18850: the console's board page needs same-origin access to the board.
// The console is a static export calling the agent it was served from
// (ADR-0004), so the agent proxies the OPERATOR verbs -- list sprints, list a
// sprint's tickets, requeue, resolve. Claim is deliberately NOT proxied:
// claiming is what agents do against the board directly, and giving a
// browser a same-origin claim route would blur that boundary for no need.
func TestBoardProxy_ForwardsOperatorVerbs(t *testing.T) {
	t.Parallel()
	var got []string
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" body="+readAll(r.Body))
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusOK
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/tickets/T9/requeue" {
			status = http.StatusConflict
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer board.Close()

	mux := http.NewServeMux()
	MountBoardProxy(mux, board.URL, os.Getenv("SPRINTBOARD_API_TOKEN"))

	cases := []struct {
		method, path, body string
		wantStatus         int
		wantForwarded      string
	}{
		{http.MethodGet, "/api/v1/board/sprints", "", http.StatusOK, "GET /api/v1/sprints body="},
		{http.MethodGet, "/api/v1/board/sprints/S1/tickets", "", http.StatusOK, "GET /api/v1/sprints/S1/tickets body="},
		{http.MethodPost, "/api/v1/board/tickets/T1/requeue", `{"actor":"op","reason":"r"}`, http.StatusOK, "POST /api/v1/tickets/T1/requeue body=" + `{"actor":"op","reason":"r"}`},
		{http.MethodPost, "/api/v1/board/tickets/T2/resolve", `{"actor":"op","reason":"r"}`, http.StatusOK, "POST /api/v1/tickets/T2/resolve body=" + `{"actor":"op","reason":"r"}`},
		{http.MethodPost, "/api/v1/board/tickets/T9/requeue", `{"actor":"op","reason":"r"}`, http.StatusConflict, "POST /api/v1/tickets/T9/requeue body=" + `{"actor":"op","reason":"r"}`},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, body))
		if rec.Code != tc.wantStatus {
			t.Fatalf("%s %s = %d, want %d (board status must pass through verbatim)", tc.method, tc.path, rec.Code, tc.wantStatus)
		}
		last := got[len(got)-1]
		if last != tc.wantForwarded {
			t.Fatalf("forwarded %q, want %q", last, tc.wantForwarded)
		}
	}
}

func TestBoardProxy_RefusesEverythingElse(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	MountBoardProxy(mux, "http://127.0.0.1:1", "")

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/board/tickets/T1/claim"},
		{http.MethodPost, "/api/v1/board/tickets/T1/complete"},
		{http.MethodDelete, "/api/v1/board/sprints/S1"},
		{http.MethodGet, "/api/v1/board/tickets/T1/comments"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404 (only the operator verbs are proxied)", tc.method, tc.path, rec.Code)
		}
	}
}

func TestBoardProxy_BoardDownIsBadGateway(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	MountBoardProxy(mux, "http://127.0.0.1:1", "") // nothing listens here

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/board/sprints", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("board unreachable = %d, want 502 with a JSON error body", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sprintboard") {
		t.Fatalf("502 body = %q, want it to name the upstream", rec.Body.String())
	}
}

func readAll(r io.Reader) string {
	if r == nil {
		return ""
	}
	b, _ := io.ReadAll(r)
	return string(b)
}

// v18851: when the board's shared bearer is provisioned the proxy must
// present it, so the console's traffic counts as authenticated in bootstrap
// mode (and keeps working the day the operator flips to required). No token
// configured -> no header, exactly as before. Not parallel: t.Setenv.
func TestBoardProxy_PresentsSharedBearerWhenProvisioned(t *testing.T) {
	var auth string
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer board.Close()

	// Synthetic low-entropy value built at runtime so secret scanners see no
	// literal; the board only requires >=32 bytes, not entropy, in tests.
	shared := strings.Repeat("test-bearer-", 4)
	t.Setenv("SPRINTBOARD_API_TOKEN", shared)
	mux := http.NewServeMux()
	MountBoardProxy(mux, board.URL, os.Getenv("SPRINTBOARD_API_TOKEN"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/board/sprints", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if auth != "Bearer "+shared {
		t.Fatalf("Authorization forwarded = %q, want the shared bearer", auth)
	}
}

func TestBoardProxy_NoHeaderWhenTokenUnset(t *testing.T) {
	var auth = "sentinel"
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer board.Close()

	t.Setenv("SPRINTBOARD_API_TOKEN", "")
	mux := http.NewServeMux()
	MountBoardProxy(mux, board.URL, os.Getenv("SPRINTBOARD_API_TOKEN"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/board/sprints", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if auth != "" {
		t.Fatalf("Authorization forwarded = %q, want none when unprovisioned", auth)
	}
}

// v18851 poll-now: the console needs a launch button. POST /api/v1/board/
// poll-now nudges the ticket poller out of its idle backoff via a callback
// supplied by main (the dashboard package must not import the runtime). When
// ticket polling is not enabled the route answers 503 so the console can say
// so instead of showing a button that silently does nothing.
func TestPollNow_Nudges(t *testing.T) {
	t.Parallel()
	called := 0
	mux := http.NewServeMux()
	MountPollNow(mux, func() bool { called++; return true })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/board/poll-now", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if called != 1 {
		t.Fatalf("nudge called %d times, want 1", called)
	}
}

func TestPollNow_NoPollerAnswers503(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	MountPollNow(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/board/poll-now", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when ticket polling is off", rec.Code)
	}
}

func TestPollNow_RejectsNonPOST(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	MountPollNow(mux, func() bool { return true })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/board/poll-now", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
