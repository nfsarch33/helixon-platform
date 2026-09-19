package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// v18850: the sprint fetcher now speaks the routes the live board actually
// serves (verified against the running API): GET /api/v1/sprints to find the
// active sprint, then GET /api/v1/sprints/{id}/tickets for the tally. The old
// code called /api/v1/sprints/active -- a literal id the wildcard route
// resolves to a 404 -- with a response shape no board version ever returned.

func newBoardServer(t *testing.T, requests *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/sprints":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"count": 2,
				"sprints": []map[string]any{
					{"id": "s-closed", "name": "old", "status": "closed"},
					{"id": "s-live", "name": "v18850 run", "status": "active"},
				},
			})
		case r.URL.Path == "/api/v1/sprints/s-live/tickets":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sprint_id": "s-live",
				"tickets": []map[string]any{
					{"id": "T1", "status": "done"},
					{"id": "T2", "status": "done"},
					{"id": "T3", "status": "resolved_by_human"},
					{"id": "T4", "status": "in_progress"},
					{"id": "T5", "status": "ready"},
					{"id": "T6", "status": "backlog"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSprintProgressFetcher_UsesActiveSprintAndRealRoutes(t *testing.T) {
	t.Parallel()
	var requests []string
	srv := newBoardServer(t, &requests)
	defer srv.Close()

	got, err := NewSprintProgressFetcher(srv.URL).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(requests) != 2 || requests[0] != "GET /api/v1/sprints" || requests[1] != "GET /api/v1/sprints/s-live/tickets" {
		t.Fatalf("board requests = %v, want sprints list then the ACTIVE sprint's tickets", requests)
	}
	if got.SprintID != "s-live" || got.SprintName != "v18850 run" {
		t.Fatalf("sprint = %q/%q, want the active sprint s-live", got.SprintID, got.SprintName)
	}
	if got.TotalTickets != 6 {
		t.Fatalf("total = %d, want 6", got.TotalTickets)
	}
	if got.DoneTickets != 2 {
		t.Fatalf("done = %d, want 2 (resolved_by_human is a write-off, not a delivery)", got.DoneTickets)
	}
	if got.InProgress != 1 {
		t.Fatalf("in_progress = %d, want 1", got.InProgress)
	}
	if got.Pending != 3 {
		t.Fatalf("pending = %d, want 3 (ready+backlog+resolved_by_human)", got.Pending)
	}
	if got.Completion < 33.3 || got.Completion > 33.4 {
		t.Fatalf("completion = %v, want 2/6", got.Completion)
	}
}

func TestSprintProgressFetcher_NoActiveSprintFallsBackToNewest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/sprints" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"sprints": []map[string]any{
					{"id": "s-only", "name": "planned", "status": "planned"},
				},
			})
			return
		}
		if r.URL.Path == "/api/v1/sprints/s-only/tickets" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sprint_id": "s-only",
				"tickets":   []map[string]any{{"id": "T1", "status": "ready"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	got, err := NewSprintProgressFetcher(srv.URL).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.SprintID != "s-only" || got.TotalTickets != 1 || got.Pending != 1 {
		t.Fatalf("fallback sprint = %+v, want s-only with 1 pending ticket", got)
	}
}

func TestSprintProgressFetcher_NoSprintsIsHonestError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 0, "sprints": []map[string]any{}})
	}))
	defer srv.Close()

	if _, err := NewSprintProgressFetcher(srv.URL).Fetch(context.Background()); err == nil {
		t.Fatal("Fetch with zero sprints = nil error; an empty board must be an honest error, not a zeroed success")
	}
}

func TestSprintProgressFetcher_DefaultURLIsLiveBoardPort(t *testing.T) {
	t.Parallel()
	f := NewSprintProgressFetcher("")
	if f.sprintboardURL != "http://127.0.0.1:9400" {
		t.Fatalf("default sprintboardURL = %q, want the live board port http://127.0.0.1:9400", f.sprintboardURL)
	}
}

func TestAgentWorkloadFetcher_DefaultURLIsLiveBoardPort(t *testing.T) {
	t.Parallel()
	f := NewAgentWorkloadFetcher("")
	if f.sprintboardURL != "http://127.0.0.1:9400" {
		t.Fatalf("default sprintboardURL = %q, want the live board port http://127.0.0.1:9400", f.sprintboardURL)
	}
}
