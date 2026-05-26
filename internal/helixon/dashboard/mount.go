package dashboard

import "net/http"

// DashboardConfig holds the configuration for the dashboard endpoints.
type DashboardConfig struct {
	SprintboardURL string
	GitLabURL      string
	GitLabToken    string
	GitLabProject  string
}

// MountAll registers all dashboard endpoints on the given mux:
//   - /api/v1/dashboard - runtime overview
//   - /api/v1/agents - agent workload from SprintBoard
//   - /api/v1/cicd - CI/CD pipeline status from GitLab
//   - /api/v1/sprint - sprint progress from SprintBoard
func MountAll(mux *http.ServeMux, rv RuntimeView, cfg DashboardConfig) {
	if mux == nil {
		return
	}
	Mount(mux, rv)

	agentFetcher := NewAgentWorkloadFetcher(cfg.SprintboardURL)
	mux.Handle("/api/v1/agents", AgentWorkloadHandler(agentFetcher))

	cicdFetcher := NewCICDStatusFetcher(CICDConfig{
		GitLabURL:    cfg.GitLabURL,
		PrivateToken: cfg.GitLabToken,
		ProjectID:    cfg.GitLabProject,
	})
	mux.Handle("/api/v1/cicd", CICDStatusHandler(cicdFetcher))

	sprintFetcher := NewSprintProgressFetcher(cfg.SprintboardURL)
	mux.Handle("/api/v1/sprint", SprintProgressHandler(sprintFetcher))
}
