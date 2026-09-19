package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The tally logic against the live board contract is covered in
// sprint_progress_v2_test.go. This file keeps the transport-error and
// handler-level coverage. The previous two fake-shape tests (a sprint object
// carrying total/done/progress fields) were removed in v18850: no board
// version ever served that shape, so they tested the mock, not the system.

func TestSprintProgressFetcher_ServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:revive // unused-parameter required by interface
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no active sprint"))
	}))
	defer func() { srv.Close() }()

	fetcher := NewSprintProgressFetcher(srv.URL)
	if _, err := fetcher.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestSprintProgressHandler_GET(t *testing.T) {
	t.Parallel()
	var requests []string
	srv := newBoardServer(t, &requests)
	defer srv.Close()

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
