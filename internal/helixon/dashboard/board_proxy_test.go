package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
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
	MountBoardProxy(mux, board.URL)

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
	MountBoardProxy(mux, "http://127.0.0.1:1")

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
	MountBoardProxy(mux, "http://127.0.0.1:1") // nothing listens here

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
