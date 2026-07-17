package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCICDStatusFetcher_Success(t *testing.T) {
	t.Parallel()
	pipelines := []PipelineInfo{
		{ID: 1, Status: "success", Ref: "main"},
		{ID: 2, Status: "failed", Ref: "feat/x"},
		{ID: 3, Status: "success", Ref: "main"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pipelines)
	}))
	defer func() { srv.Close() }()

	fetcher := NewCICDStatusFetcher(CICDConfig{GitLabURL: srv.URL})
	resp, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TotalCount != 3 {
		t.Errorf("TotalCount = %d, want 3", resp.TotalCount)
	}
	want := 2.0 / 3.0 * 100
	if resp.SuccessRate < want-0.1 || resp.SuccessRate > want+0.1 {
		t.Errorf("SuccessRate = %.1f, want ~%.1f", resp.SuccessRate, want)
	}
}

func TestCICDStatusFetcher_Empty(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer func() { srv.Close() }()

	fetcher := NewCICDStatusFetcher(CICDConfig{GitLabURL: srv.URL})
	resp, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.TotalCount != 0 {
		t.Errorf("TotalCount = %d, want 0", resp.TotalCount)
	}
	if resp.SuccessRate != 0 {
		t.Errorf("SuccessRate = %f, want 0", resp.SuccessRate)
	}
}

func TestCICDStatusFetcher_WithToken(t *testing.T) {
	t.Parallel()
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		json.NewEncoder(w).Encode([]PipelineInfo{})
	}))
	defer func() { srv.Close() }()

	fetcher := NewCICDStatusFetcher(CICDConfig{
		GitLabURL:    srv.URL,
		PrivateToken: "glpat-test",
	})
	_, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotToken != "glpat-test" {
		t.Errorf("token = %q, want glpat-test", gotToken)
	}
}

func TestCICDStatusHandler_GET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]PipelineInfo{{ID: 1, Status: "success"}})
	}))
	defer func() { srv.Close() }()

	handler := CICDStatusHandler(NewCICDStatusFetcher(CICDConfig{GitLabURL: srv.URL}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cicd", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestCICDStatusHandler_RejectsNonGET(t *testing.T) {
	t.Parallel()
	handler := CICDStatusHandler(NewCICDStatusFetcher(CICDConfig{GitLabURL: "http://nowhere"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
