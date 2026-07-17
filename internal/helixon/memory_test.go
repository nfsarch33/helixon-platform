package helixon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMemorySearch_Success(t *testing.T) {
	t.Parallel()

	expected := []Memory{
		{ID: "mem-1", Content: "The user prefers dark mode", Score: 0.92},
		{ID: "mem-2", Content: "Agent was deployed on 2026-05-20", Score: 0.78},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/memories/search/" {
			t.Errorf("expected /v1/memories/search/, got %s", r.URL.Path)
		}

		var body mem0SearchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Query != "dark mode" {
			t.Errorf("query = %q, want dark mode", body.Query)
		}
		if body.AgentID != "test-agent" {
			t.Errorf("agent_id = %q, want test-agent", body.AgentID)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer func() { srv.Close() }()

	mt := NewMemoryTool(MemoryToolConfig{
		BaseURL: srv.URL,
		AgentID: "test-agent",
	})

	results, err := mt.Search(context.Background(), "dark mode")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].ID != "mem-1" {
		t.Errorf("results[0].ID = %q, want mem-1", results[0].ID)
	}
	if results[1].Content != "Agent was deployed on 2026-05-20" {
		t.Errorf("results[1].Content mismatch")
	}
}

func TestMemorySearch_Timeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { srv.Close() }()

	mt := NewMemoryTool(MemoryToolConfig{
		BaseURL: srv.URL,
		AgentID: "slow-agent",
		Timeout: 50 * time.Millisecond,
	})

	ctx := context.Background()
	_, err := mt.Search(ctx, "anything")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestMemorySearch_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"detail":"internal error"}`))
	}))
	defer func() { srv.Close() }()

	mt := NewMemoryTool(MemoryToolConfig{BaseURL: srv.URL})

	_, err := mt.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestMemoryAdd_InferFalse(t *testing.T) {
	t.Parallel()

	var receivedBody mem0AddRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/memories/" {
			t.Errorf("expected /v1/memories/, got %s", r.URL.Path)
		}

		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Fatalf("decode: %v", err)
		}

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"new-mem-1"}`))
	}))
	defer func() { srv.Close() }()

	mt := NewMemoryTool(MemoryToolConfig{
		BaseURL: srv.URL,
		AgentID: "writer-agent",
	})

	err := mt.Add(context.Background(), "The deployment succeeded at 14:00", false)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if receivedBody.Infer {
		t.Error("expected infer=false")
	}
	if receivedBody.AgentID != "writer-agent" {
		t.Errorf("agent_id = %q, want writer-agent", receivedBody.AgentID)
	}
	if len(receivedBody.Messages) != 1 || receivedBody.Messages[0].Content != "The deployment succeeded at 14:00" {
		t.Errorf("message content mismatch: %+v", receivedBody.Messages)
	}
}

func TestMemoryAdd_InferTrue(t *testing.T) {
	t.Parallel()

	var receivedBody mem0AddRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"inferred-1"}`))
	}))
	defer func() { srv.Close() }()

	mt := NewMemoryTool(MemoryToolConfig{
		BaseURL: srv.URL,
		AgentID: "infer-agent",
	})

	err := mt.Add(context.Background(), "Jason prefers Go over Python for backend services", true)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if !receivedBody.Infer {
		t.Error("expected infer=true")
	}
}
