package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// v18836: the board is gaining a shared bearer. Every verb the client speaks
// must carry it, and the absence of a token must send NO header (the control),
// because an empty "Bearer " is rejected by the board, not treated as
// anonymous.

// A repeated byte on purpose: a hex-looking fixture reads as a credential to
// a secret scanner and fails the full-history gate for every commit after it.
var testBoardToken = strings.Repeat("t", 38)

type headerSpy struct {
	mu   sync.Mutex
	seen map[string]string // "METHOD path" -> Authorization header
}

func newHeaderSpy() (*headerSpy, *httptest.Server) {
	s := &headerSpy{seen: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen[r.Method+" "+r.URL.Path] = r.Header.Get("Authorization")
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/tickets/search":
			_ = json.NewEncoder(w).Encode(map[string]any{"tickets": []Ticket{}, "count": 0})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "s1"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		}
	}))
	return s, srv
}

func driveEveryVerb(t *testing.T, c *SprintboardClient) {
	t.Helper()
	ctx := context.Background()
	if err := c.Register(ctx); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := c.SearchTickets(ctx, TicketFilter{Status: "ready"}); err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if err := c.ClaimTicket(ctx, "T-1"); err != nil {
		t.Fatalf("ClaimTicket: %v", err)
	}
	if err := c.CompleteTicket(ctx, "T-1", "done"); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}
	if err := c.AddComment(ctx, "T-1", "a", "b"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, err := c.SprintStatus(ctx, "s1"); err != nil {
		t.Fatalf("SprintStatus: %v", err)
	}
}

var everyVerb = []string{
	"POST /api/v1/agents",
	"GET /api/v1/tickets/search",
	"POST /api/v1/tickets/T-1/claim",
	"POST /api/v1/tickets/T-1/complete",
	"POST /api/v1/tickets/T-1/comments",
	"GET /api/v1/sprints/s1",
}

func TestSprintboardClient_SendsBearerOnEveryVerb(t *testing.T) {
	t.Parallel()
	spy, srv := newHeaderSpy()
	defer srv.Close()
	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "w", Token: testBoardToken}, nil)
	driveEveryVerb(t, c)
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.seen) != len(everyVerb) {
		t.Fatalf("server saw %d distinct requests, want %d: %v", len(spy.seen), len(everyVerb), spy.seen)
	}
	for _, verb := range everyVerb {
		if got := spy.seen[verb]; got != "Bearer "+testBoardToken {
			t.Errorf("%s: Authorization = %q, want the bearer", verb, got)
		}
	}
}

// The control: with no token configured the header is ABSENT on every verb.
func TestSprintboardClient_NoTokenSendsNoHeader(t *testing.T) {
	t.Parallel()
	spy, srv := newHeaderSpy()
	defer srv.Close()
	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "w"}, nil)
	driveEveryVerb(t, c)
	spy.mu.Lock()
	defer spy.mu.Unlock()
	for _, verb := range everyVerb {
		if got, ok := spy.seen[verb]; !ok {
			t.Errorf("%s: never reached the server", verb)
		} else if got != "" {
			t.Errorf("%s: Authorization = %q, want none", verb, got)
		}
	}
}

// A board in required mode answers 401; the client must surface it as an
// error naming the status, not swallow it as an empty result.
func TestSprintboardClient_401SurfacesOnClaimAndSearch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"missing bearer token"}`))
	}))
	defer srv.Close()
	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "w"}, nil)
	if err := c.ClaimTicket(context.Background(), "T-1"); err == nil || !contains(err.Error(), "401") {
		t.Fatalf("ClaimTicket err = %v, want an error naming 401", err)
	}
	if _, err := c.SearchTickets(context.Background(), TicketFilter{}); err == nil || !contains(err.Error(), "401") {
		t.Fatalf("SearchTickets err = %v, want an error naming 401", err)
	}
}
