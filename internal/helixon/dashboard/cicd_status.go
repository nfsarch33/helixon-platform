package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PipelineInfo represents a GitLab CI/CD pipeline status.
type PipelineInfo struct {
	ID        int    `json:"id"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha,omitempty"`
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	WebURL    string `json:"web_url,omitempty"`
}

// CICDStatusResponse is the JSON payload for /api/v1/cicd.
type CICDStatusResponse struct {
	Pipelines   []PipelineInfo `json:"pipelines"`
	TotalCount  int            `json:"total_count"`
	SuccessRate float64        `json:"success_rate"`
	GeneratedAt string         `json:"generated_at"`
}

// CICDStatusFetcher queries GitLab for recent pipeline statuses.
type CICDStatusFetcher struct {
	gitlabURL    string
	privateToken string
	projectID    string
	client       *http.Client
}

// CICDConfig configures the GitLab CI/CD fetcher.
type CICDConfig struct {
	GitLabURL    string
	PrivateToken string
	ProjectID    string
}

// NewCICDStatusFetcher creates a fetcher for GitLab pipeline statuses.
func NewCICDStatusFetcher(cfg CICDConfig) *CICDStatusFetcher {
	if cfg.GitLabURL == "" {
		cfg.GitLabURL = "http://localhost:30080"
	}
	if cfg.ProjectID == "" {
		cfg.ProjectID = "1"
	}
	return &CICDStatusFetcher{
		gitlabURL:    cfg.GitLabURL,
		privateToken: cfg.PrivateToken,
		projectID:    cfg.ProjectID,
		client:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Fetch retrieves the latest pipeline statuses from GitLab.
func (f *CICDStatusFetcher) Fetch(ctx context.Context) (*CICDStatusResponse, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%s/pipelines?per_page=10&order_by=id&sort=desc", f.gitlabURL, f.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cicd status: build request: %w", err)
	}
	if f.privateToken != "" {
		req.Header.Set("PRIVATE-TOKEN", f.privateToken)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cicd status: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("cicd status: read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("cicd status: status %d: %s", resp.StatusCode, string(data))
	}

	var pipelines []PipelineInfo
	if err := json.Unmarshal(data, &pipelines); err != nil {
		return nil, fmt.Errorf("cicd status: decode: %w", err)
	}

	successCount := 0
	for _, p := range pipelines {
		if p.Status == "success" {
			successCount++
		}
	}

	var successRate float64
	if len(pipelines) > 0 {
		successRate = float64(successCount) / float64(len(pipelines)) * 100
	}

	return &CICDStatusResponse{
		Pipelines:   pipelines,
		TotalCount:  len(pipelines),
		SuccessRate: successRate,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// CICDStatusHandler returns an HTTP handler for /api/v1/cicd.
func CICDStatusHandler(fetcher *CICDStatusFetcher) http.Handler {
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
