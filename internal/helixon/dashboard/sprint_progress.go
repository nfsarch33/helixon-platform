package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SprintProgressResponse is the JSON payload for /api/v1/sprint.
type SprintProgressResponse struct {
	SprintID     string  `json:"sprint_id"`
	SprintName   string  `json:"sprint_name,omitempty"`
	TenantID     string  `json:"tenant_id,omitempty"` // v18686-1: multi-tenancy
	TotalTickets int     `json:"total_tickets"`
	DoneTickets  int     `json:"done_tickets"`
	InProgress   int     `json:"in_progress"`
	Pending      int     `json:"pending"`
	Completion   float64 `json:"completion_pct"`
	GeneratedAt  string  `json:"generated_at"`
}

// SprintProgressFetcher queries SprintBoard for sprint completion data.
type SprintProgressFetcher struct {
	sprintboardURL string
	client         *http.Client
}

// NewSprintProgressFetcher creates a fetcher for sprint progress data.
// The default is the live board API port (verified 2026-09-19: the API
// listens on 9400; the old 8585 default pointed at nothing).
func NewSprintProgressFetcher(sprintboardURL string) *SprintProgressFetcher {
	if sprintboardURL == "" {
		sprintboardURL = "http://127.0.0.1:9400"
	}
	return &SprintProgressFetcher{
		sprintboardURL: sprintboardURL,
		client:         &http.Client{Timeout: 5 * time.Second},
	}
}

// sprintSummary is one row of GET /api/v1/sprints.
type sprintSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// boardTicketStatus is the status field of GET /api/v1/sprints/{id}/tickets.
type boardTicketStatus struct {
	Status string `json:"status"`
}

// Fetch retrieves the current sprint progress from SprintBoard.
//
// It uses the routes the live board actually serves, in two steps: list the
// sprints and pick the active one (falling back to the only/newest sprint
// when none is active), then tally that sprint's tickets. The previous
// single call to /api/v1/sprints/active hit the {id} wildcard with the
// literal id "active" and 404'd from the database on every call.
func (f *SprintProgressFetcher) Fetch(ctx context.Context) (*SprintProgressResponse, error) {
	sprints, err := f.getJSON(ctx, "/api/v1/sprints", func() any {
		return &struct {
			Sprints []sprintSummary `json:"sprints"`
		}{}
	})
	if err != nil {
		return nil, fmt.Errorf("sprint progress: list sprints: %w", err)
	}
	list := sprints.(*struct {
		Sprints []sprintSummary `json:"sprints"`
	}).Sprints

	sprint := pickActiveSprint(list)
	if sprint == nil {
		return nil, fmt.Errorf("sprint progress: board has no sprints")
	}

	ticketsRaw, err := f.getJSON(ctx, "/api/v1/sprints/"+sprint.ID+"/tickets", func() any {
		return &struct {
			Tickets []boardTicketStatus `json:"tickets"`
		}{}
	})
	if err != nil {
		return nil, fmt.Errorf("sprint progress: tickets for %s: %w", sprint.ID, err)
	}
	tickets := ticketsRaw.(*struct {
		Tickets []boardTicketStatus `json:"tickets"`
	}).Tickets

	total := len(tickets)
	done, inProgress, pending := 0, 0, 0
	for _, tk := range tickets {
		switch tk.Status {
		case "done":
			done++
		case "in_progress":
			inProgress++
		default:
			// backlog, ready, review, blocked, ready_for_handoff -- not
			// finished and not being worked. resolved_by_human lands here
			// too: it closes a ticket a human wrote off, which the board
			// deliberately distinguishes from a delivery, so it must not
			// inflate the completion percentage.
			pending++
		}
	}

	var completion float64
	if total > 0 {
		completion = float64(done) / float64(total) * 100
	}

	return &SprintProgressResponse{
		SprintID:     sprint.ID,
		SprintName:   sprint.Name,
		TotalTickets: total,
		DoneTickets:  done,
		InProgress:   inProgress,
		Pending:      pending,
		Completion:   completion,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// pickActiveSprint returns the first active sprint, or the first sprint when
// none carries status "active" (a board mid-planning still has one sprint
// worth showing). Nil only when the list is empty.
func pickActiveSprint(sprints []sprintSummary) *sprintSummary {
	for i := range sprints {
		if sprints[i].Status == "active" {
			return &sprints[i]
		}
	}
	if len(sprints) > 0 {
		return &sprints[0]
	}
	return nil
}

// getJSON GETs path from the board and decodes into a fresh target from newTarget.
func (f *SprintProgressFetcher) getJSON(ctx context.Context, path string, newTarget func() any) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.sprintboardURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	}

	target := newTarget()
	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return target, nil
}

// SprintProgressHandler returns an HTTP handler for /api/v1/sprint.
func SprintProgressHandler(fetcher *SprintProgressFetcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		resp, err := fetcher.Fetch(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("fetch error: %v", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
