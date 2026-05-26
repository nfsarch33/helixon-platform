package helixon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSprintboardClient_Register_Success(t *testing.T) {
	t.Parallel()

	var received registerRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/agents/register" {
			t.Errorf("path = %q, want /api/v1/agents/register", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{
		BaseURL: srv.URL,
		AgentID: "agent-alpha",
	})

	err := client.Register(context.Background(), "agent-alpha")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if received.AgentID != "agent-alpha" {
		t.Errorf("agent_id = %q, want agent-alpha", received.AgentID)
	}
}

func TestSprintboardClient_Register_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"maintenance"}`))
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL})
	err := client.Register(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
	}
}

func TestSprintboardClient_Heartbeat_Success(t *testing.T) {
	t.Parallel()

	var received heartbeatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/heartbeat" {
			t.Errorf("path = %q, want /api/v1/agents/heartbeat", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL})
	err := client.Heartbeat(context.Background(), "agent-beta")
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if received.AgentID != "agent-beta" {
		t.Errorf("agent_id = %q, want agent-beta", received.AgentID)
	}
	if received.Status != "alive" {
		t.Errorf("status = %q, want alive", received.Status)
	}
}

func TestSprintboardClient_Heartbeat_Timeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{
		BaseURL: srv.URL,
		Timeout: 50 * time.Millisecond,
	})

	err := client.Heartbeat(context.Background(), "slow")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestSprintboardClient_ClaimTicket_Success(t *testing.T) {
	t.Parallel()

	var received claimRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tickets/claim" {
			t.Errorf("path = %q, want /api/v1/tickets/claim", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"claimed":true}`))
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL})
	err := client.ClaimTicket(context.Background(), "T-8001", "agent-gamma")
	if err != nil {
		t.Fatalf("ClaimTicket: %v", err)
	}
	if received.TicketID != "T-8001" {
		t.Errorf("ticket_id = %q, want T-8001", received.TicketID)
	}
	if received.AgentID != "agent-gamma" {
		t.Errorf("agent_id = %q, want agent-gamma", received.AgentID)
	}
}

func TestSprintboardClient_ClaimTicket_AlreadyClaimed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"ticket already claimed by agent-delta"}`))
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL})
	err := client.ClaimTicket(context.Background(), "T-8001", "agent-gamma")
	if err == nil {
		t.Fatal("expected error for conflict, got nil")
	}
}
