package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentWorkloadFetcher_Success(t *testing.T) {
	t.Parallel()
	agents := []AgentInfo{
		{AgentID: "fleet-daemon-wsl1", Status: "active", CurrentTask: "TASK-001"},
		{AgentID: "helixon-claude", Status: "active"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agents)
	}))
	defer srv.Close()

	fetcher := NewAgentWorkloadFetcher(srv.URL)
	resp, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TotalAgents != 2 {
		t.Errorf("TotalAgents = %d, want 2", resp.TotalAgents)
	}
	if resp.ActiveTasks != 1 {
		t.Errorf("ActiveTasks = %d, want 1", resp.ActiveTasks)
	}
	if resp.GeneratedAt == "" {
		t.Error("GeneratedAt empty")
	}
}

func TestAgentWorkloadFetcher_WrappedResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"agents": []AgentInfo{{AgentID: "a1", Status: "active"}},
		})
	}))
	defer srv.Close()

	fetcher := NewAgentWorkloadFetcher(srv.URL)
	resp, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TotalAgents != 1 {
		t.Errorf("TotalAgents = %d, want 1", resp.TotalAgents)
	}
}

func TestAgentWorkloadFetcher_ServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("db error"))
	}))
	defer srv.Close()

	fetcher := NewAgentWorkloadFetcher(srv.URL)
	_, err := fetcher.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestAgentWorkloadHandler_GET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]AgentInfo{{AgentID: "x", Status: "active"}})
	}))
	defer srv.Close()

	handler := AgentWorkloadHandler(NewAgentWorkloadFetcher(srv.URL))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var resp AgentWorkloadResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalAgents != 1 {
		t.Errorf("TotalAgents = %d", resp.TotalAgents)
	}
}

func TestAgentWorkloadHandler_RejectsNonGET(t *testing.T) {
	t.Parallel()
	handler := AgentWorkloadHandler(NewAgentWorkloadFetcher("http://nowhere"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/agents", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
