package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v18779: the client gained the routes the poller needs and, more
// importantly, the ability to tell a lost claim race apart from a broken
// board. Every path asserted here is one the LIVE server actually serves.

func TestSprintboardDefaultBaseURLIsTheLiveBoard(t *testing.T) {
	t.Parallel()
	c := NewSprintboardClient(SprintboardConfig{}, nil)
	if c.cfg.BaseURL != DefaultSprintboardURL {
		t.Fatalf("default BaseURL = %q, want %q", c.cfg.BaseURL, DefaultSprintboardURL)
	}
	if c.cfg.BaseURL != "http://127.0.0.1:9400" {
		t.Fatalf("default BaseURL = %q; the board listens on :9400, not :8585", c.cfg.BaseURL)
	}
}

func TestSprintboardSearchTickets(t *testing.T) {
	t.Parallel()
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tickets": []Ticket{{
				ID: "T-1", Title: "fix it", Status: "ready", Priority: 3,
				AcceptanceCriteria: "tests pass", Labels: []string{"go"},
			}},
			"count": 1,
		})
	}))
	defer srv.Close()

	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "w"}, nil)
	got, err := c.SearchTickets(context.Background(), TicketFilter{
		Status: "ready", PriorityMin: 2, Limit: 5, Labels: []string{"go", "urgent"}, SprintID: "v1",
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if gotPath != "/api/v1/tickets/search" {
		t.Fatalf("path = %q, want /api/v1/tickets/search", gotPath)
	}
	for _, want := range []string{"status=ready", "priority_min=2", "limit=5", "label=go", "label=urgent", "sprint_id=v1"} {
		if !contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if len(got) != 1 || got[0].ID != "T-1" || got[0].AcceptanceCriteria != "tests pass" {
		t.Fatalf("decoded tickets = %+v", got)
	}
}

func TestSprintboardSearchTicketsErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, `{"error":"boom"}`},
		{"garbage body", http.StatusOK, `not json`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL}, nil)
			if _, err := c.SearchTickets(context.Background(), TicketFilter{}); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestSprintboardClaimConflictIsTyped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    error
		wantNoErr  bool
		wantHolder string
	}{
		{
			name: "409 is a conflict", status: http.StatusConflict,
			body:       `{"success":false,"ticket_id":"T-1","claimed_by":"other-agent"}`,
			wantErr:    ErrClaimConflict,
			wantHolder: "other-agent",
		},
		{
			name: "200 with success=false is also a conflict", status: http.StatusOK,
			body:       `{"success":false,"ticket_id":"T-1","claimed_by":"other-agent"}`,
			wantErr:    ErrClaimConflict,
			wantHolder: "other-agent",
		},
		{
			name: "200 success", status: http.StatusOK,
			body:      `{"success":true,"ticket_id":"T-1","claimed_by":"me"}`,
			wantNoErr: true,
		},
		{
			name: "500 is NOT a conflict", status: http.StatusInternalServerError,
			body: `{"error":"db locked"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/tickets/T-1/claim" {
					t.Errorf("path = %q, want /api/v1/tickets/T-1/claim", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "me"}, nil)
			err := c.ClaimTicket(context.Background(), "T-1")
			switch {
			case tc.wantNoErr:
				if err != nil {
					t.Fatalf("ClaimTicket: %v", err)
				}
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want wrapped %v", err, tc.wantErr)
				}
				if tc.wantHolder != "" && !contains(err.Error(), tc.wantHolder) {
					t.Errorf("err %q does not name the holder %q", err, tc.wantHolder)
				}
			default:
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if errors.Is(err, ErrClaimConflict) {
					t.Fatalf("a 500 must not be reported as a lost race: %v", err)
				}
			}
		})
	}
}

func TestSprintboardCompleteSendsEvidence(t *testing.T) {
	t.Parallel()
	var body struct {
		AgentID  string `json:"agent_id"`
		Evidence string `json:"evidence"`
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "worker-1"}, nil)
	if err := c.CompleteTicket(context.Background(), "T-9", "verifier passed: go test ./... ok"); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}
	if path != "/api/v1/tickets/T-9/complete" {
		t.Fatalf("path = %q", path)
	}
	if body.AgentID != "worker-1" || body.Evidence != "verifier passed: go test ./... ok" {
		t.Fatalf("payload = %+v", body)
	}
}

func TestSprintboardAddComment(t *testing.T) {
	t.Parallel()
	var body struct {
		Author string `json:"author"`
		Body   string `json:"body"`
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "worker-1"}, nil)
	if err := c.AddComment(context.Background(), "T-9", "worker-1", "escalating"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if path != "/api/v1/tickets/T-9/comments" {
		t.Fatalf("path = %q, want /api/v1/tickets/T-9/comments", path)
	}
	if body.Author != "worker-1" || body.Body != "escalating" {
		t.Fatalf("payload = %+v", body)
	}
	// The board rejects an empty author or body with a 400; catching it here
	// keeps the escalation path from turning into a silent no-op.
	if err := c.AddComment(context.Background(), "T-9", "", "x"); err == nil {
		t.Error("expected an error for an empty author")
	}
	if err := c.AddComment(context.Background(), "T-9", "a", ""); err == nil {
		t.Error("expected an error for an empty body")
	}
}

func TestSprintboardAddCommentServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "w"}, nil)
	if err := c.AddComment(context.Background(), "T-9", "w", "b"); err == nil {
		t.Fatal("expected an error for a 500")
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
