package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSprintProgressFetcher_WithTickets(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   "sprint-42",
			"name": "v11400",
			"tickets": []map[string]string{
				{"status": "done"},
				{"status": "done"},
				{"status": "in_progress"},
				{"status": "pending"},
				{"status": "pending"},
			},
		})
	}))
	defer func() { srv.Close() }()

	fetcher := NewSprintProgressFetcher(srv.URL)
	resp, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.SprintID != "sprint-42" {
		t.Errorf("SprintID = %q", resp.SprintID)
	}
	if resp.TotalTickets != 5 {
		t.Errorf("TotalTickets = %d, want 5", resp.TotalTickets)
	}
	if resp.DoneTickets != 2 {
		t.Errorf("DoneTickets = %d, want 2", resp.DoneTickets)
	}
	if resp.InProgress != 1 {
		t.Errorf("InProgress = %d, want 1", resp.InProgress)
	}
	if resp.Pending != 2 {
		t.Errorf("Pending = %d, want 2", resp.Pending)
	}
	want := 40.0
	if resp.Completion < want-0.1 || resp.Completion > want+0.1 {
		t.Errorf("Completion = %.1f, want %.1f", resp.Completion, want)
	}
}

func TestSprintProgressFetcher_WithCounts(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "sprint-43",
			"total": 10,
			"done":  7,
		})
	}))
	defer func() { srv.Close() }()

	fetcher := NewSprintProgressFetcher(srv.URL)
	resp, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TotalTickets != 10 {
		t.Errorf("TotalTickets = %d", resp.TotalTickets)
	}
	if resp.DoneTickets != 7 {
		t.Errorf("DoneTickets = %d", resp.DoneTickets)
	}
	if resp.Completion != 70.0 {
		t.Errorf("Completion = %f, want 70", resp.Completion)
	}
}

func TestSprintProgressFetcher_ServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("no active sprint"))
	}))
	defer func() { srv.Close() }()

	fetcher := NewSprintProgressFetcher(srv.URL)
	_, err := fetcher.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestSprintProgressHandler_GET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		json.NewEncoder(w).Encode(map[string]any{"id": "s1", "total": 5, "done": 3})
	}))
	defer func() { srv.Close() }()

	handler := SprintProgressHandler(NewSprintProgressFetcher(srv.URL))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sprint", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSprintProgressHandler_RejectsNonGET(t *testing.T) {
	t.Parallel()
	handler := SprintProgressHandler(NewSprintProgressFetcher("http://nowhere"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
