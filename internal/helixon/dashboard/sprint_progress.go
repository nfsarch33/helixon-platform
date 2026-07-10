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
func NewSprintProgressFetcher(sprintboardURL string) *SprintProgressFetcher {
	if sprintboardURL == "" {
		sprintboardURL = "http://localhost:8585"
	}
	return &SprintProgressFetcher{
		sprintboardURL: sprintboardURL,
		client:         &http.Client{Timeout: 5 * time.Second},
	}
}

// Fetch retrieves the current sprint progress from SprintBoard.
func (f *SprintProgressFetcher) Fetch(ctx context.Context) (*SprintProgressResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.sprintboardURL+"/api/v1/sprints/active", nil)
	if err != nil {
		return nil, fmt.Errorf("sprint progress: build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sprint progress: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("sprint progress: read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sprint progress: status %d: %s", resp.StatusCode, string(data))
	}

	var raw struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Tickets []struct {
			Status string `json:"status"`
		} `json:"tickets"`
		Total    int `json:"total"`
		Done     int `json:"done"`
		Progress int `json:"progress"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("sprint progress: decode: %w", err)
	}

	total := raw.Total
	done := raw.Done
	inProgress := 0
	pending := 0

	if total == 0 && len(raw.Tickets) > 0 {
		total = len(raw.Tickets)
		for _, t := range raw.Tickets {
			switch t.Status {
			case "done", "completed", "closed":
				done++
			case "in_progress", "active":
				inProgress++
			default:
				pending++
			}
		}
	} else if total > 0 {
		inProgress = total - done - pending
		if inProgress < 0 {
			inProgress = 0
		}
	}

	var completion float64
	if total > 0 {
		completion = float64(done) / float64(total) * 100
	}

	return &SprintProgressResponse{
		SprintID:     raw.ID,
		SprintName:   raw.Name,
		TotalTickets: total,
		DoneTickets:  done,
		InProgress:   inProgress,
		Pending:      pending,
		Completion:   completion,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
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
