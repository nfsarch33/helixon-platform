package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// bearerBoard is a board that requires the shared bearer, exactly as the
// live one does under required auth: no header (or the wrong token) is 401,
// and the presented Authorization values are recorded for assertion.
type bearerBoard struct {
	mu       sync.Mutex
	authSeen []string
}

func (b *bearerBoard) handler(t *testing.T, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		b.mu.Lock()
		b.authSeen = append(b.authSeen, auth)
		b.mu.Unlock()
		if auth != "Bearer test-bearer" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"missing bearer token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func (b *bearerBoard) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.authSeen...)
}

// TestAgentWorkloadFetcherSendsBearer: the fetcher presents the configured
// bearer on the agents call, and the route serves 200 JSON through the
// handler.
func TestAgentWorkloadFetcherSendsBearer(t *testing.T) {
	board := &bearerBoard{}
	srv := httptest.NewServer(board.handler(t, `{"agents":[{"id":"a1","current_task":""}]}`))
	defer srv.Close()

	fetcher := NewAgentWorkloadFetcher(srv.URL, "test-bearer")
	res, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch with bearer: %v", err)
	}
	if res.TotalAgents != 1 {
		t.Fatalf("TotalAgents = %d, want 1", res.TotalAgents)
	}
	if got := board.seen(); len(got) == 0 || got[0] != "Bearer test-bearer" {
		t.Fatalf("Authorization seen = %v, want Bearer test-bearer", got)
	}
}

// TestAgentWorkloadFetcherMissingTokenIsNamedError: without the token the
// board's 401 surfaces as ErrBoardUnauthorized (a named, greppable error),
// not a panic and not a bare status string.
func TestAgentWorkloadFetcherMissingTokenIsNamedError(t *testing.T) {
	board := &bearerBoard{}
	srv := httptest.NewServer(board.handler(t, `{}`))
	defer srv.Close()

	_, err := NewAgentWorkloadFetcher(srv.URL, "").Fetch(context.Background())
	if !errors.Is(err, ErrBoardUnauthorized) {
		t.Fatalf("err = %v, want ErrBoardUnauthorized", err)
	}
}

// TestSprintProgressFetcherSendsBearer: the fetcher presents the configured
// bearer on both board calls it makes (sprints list, then the tickets list).
func TestSprintProgressFetcherSendsBearer(t *testing.T) {
	board := &bearerBoard{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sprints", board.handler(t, `{"sprints":[{"id":"s1","name":"current","status":"active"}]}`))
	mux.HandleFunc("GET /api/v1/sprints/{id}/tickets", board.handler(t, `{"tickets":[]}`))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fetcher := NewSprintProgressFetcher(srv.URL, "test-bearer")
	if _, err := fetcher.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch with bearer: %v", err)
	}
	seen := board.seen()
	if len(seen) < 2 {
		t.Fatalf("board called %d times, want at least 2 (sprints then tickets)", len(seen))
	}
	for i, a := range seen {
		if a != "Bearer test-bearer" {
			t.Fatalf("call %d Authorization = %q, want Bearer test-bearer", i, a)
		}
	}
}

// TestSprintProgressFetcherMissingTokenIsNamedError: same contract on the
// sprint surface.
func TestSprintProgressFetcherMissingTokenIsNamedError(t *testing.T) {
	board := &bearerBoard{}
	srv := httptest.NewServer(board.handler(t, `{}`))
	defer srv.Close()

	if _, err := NewSprintProgressFetcher(srv.URL, "").Fetch(context.Background()); !errors.Is(err, ErrBoardUnauthorized) {
		t.Fatalf("err = %v, want ErrBoardUnauthorized", err)
	}
}
