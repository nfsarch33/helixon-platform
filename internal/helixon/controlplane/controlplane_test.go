package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestA2AClientRegister(t *testing.T) {
	var received AgentCard
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/agents/register", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewA2AClient(A2AConfig{
		GatewayURL: srv.URL,
		BearerAuth: "test-token",
	}, nil)

	err := client.Register(context.Background(), AgentCard{
		Name:         "test-agent",
		ID:           "agent-001",
		Capabilities: []string{"code", "review"},
	})
	require.NoError(t, err)
	assert.Equal(t, "test-agent", received.Name)
	assert.Equal(t, "active", received.Status)
	assert.NotEmpty(t, received.RegisteredAt)
}

func TestA2AClientCompleteTask(t *testing.T) {
	var received TaskResult
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/tasks/complete", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewA2AClient(A2AConfig{GatewayURL: srv.URL}, nil)
	err := client.CompleteTask(context.Background(), TaskResult{
		TaskID: "task-001",
		Status: "completed",
		Output: "result data",
	})
	require.NoError(t, err)
	assert.Equal(t, "task-001", received.TaskID)
	assert.Equal(t, "completed", received.Status)
}

func TestA2AClientDeregister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/agents/deregister", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewA2AClient(A2AConfig{GatewayURL: srv.URL}, nil)
	err := client.Deregister(context.Background(), "agent-001")
	require.NoError(t, err)
}

type testHeartbeatSink struct {
	mu       sync.Mutex
	payloads []HeartbeatPayload
}

func (s *testHeartbeatSink) SendHeartbeat(_ context.Context, payload HeartbeatPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, payload)
	return nil
}

func (s *testHeartbeatSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.payloads)
}

func (s *testHeartbeatSink) last() HeartbeatPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payloads[len(s.payloads)-1]
}

func (s *testHeartbeatSink) snapshot() []HeartbeatPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]HeartbeatPayload, len(s.payloads))
	copy(cp, s.payloads)
	return cp
}

func TestHeartbeatMonitorSendNow(t *testing.T) {
	sink := &testHeartbeatSink{}
	monitor := NewHeartbeatMonitor(sink, HeartbeatConfig{
		AgentID:  "test-agent",
		Interval: 1 * time.Hour,
	})

	monitor.Update(10, 5000, "sess-123")
	monitor.SetExtra("model", "qwen3:4b")

	err := monitor.SendNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, sink.count())

	payload := sink.last()
	assert.Equal(t, "test-agent", payload.AgentID)
	assert.Equal(t, "running", payload.Status)
	assert.Equal(t, 10, payload.Iterations)
	assert.Equal(t, 5000, payload.TokensUsed)
	assert.Equal(t, "sess-123", payload.ActiveSessID)
	assert.Equal(t, "qwen3:4b", payload.Extras["model"])
}

func TestHeartbeatMonitorPeriodic(t *testing.T) {
	sink := &testHeartbeatSink{}
	monitor := NewHeartbeatMonitor(sink, HeartbeatConfig{
		AgentID:  "periodic-test",
		Interval: 50 * time.Millisecond,
	})

	ctx := context.Background()
	cancel := monitor.Start(ctx)
	defer cancel()

	time.Sleep(180 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	assert.GreaterOrEqual(t, sink.count(), 2)
	for _, p := range sink.snapshot() {
		assert.Equal(t, "periodic-test", p.AgentID)
		assert.Equal(t, "running", p.Status)
	}
}

func TestHeartbeatMonitorShutdown(t *testing.T) {
	sink := &testHeartbeatSink{}
	monitor := NewHeartbeatMonitor(sink, HeartbeatConfig{AgentID: "shutdown-test"})

	err := monitor.SendShutdown(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "shutting_down", sink.last().Status)
}

func TestHeartbeatMonitorUptime(t *testing.T) {
	monitor := NewHeartbeatMonitor(&testHeartbeatSink{}, HeartbeatConfig{})
	time.Sleep(10 * time.Millisecond)
	assert.Greater(t, monitor.Uptime(), time.Duration(0))
}

func TestFleetDailyReport(t *testing.T) {
	report := FleetDailyReport("agent-1", 5, 10000, 5000, 0.50, 2*time.Hour)
	assert.Contains(t, report, "agent-1")
	assert.Contains(t, report, "Sessions: 5")
	assert.Contains(t, report, "Tokens In: 10000")
	assert.Contains(t, report, "$0.5000")
}

func TestSprintboardClientRegister(t *testing.T) {
	var received AgentRegistration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/agents", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{
		BaseURL:      srv.URL,
		AgentName:    "test-agent",
		Capabilities: "code,test,review",
	}, nil)

	err := client.Register(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-agent", received.AgentID)
	assert.Equal(t, "active", received.Status)
	assert.Equal(t, "code,test,review", received.Capabilities)
}

func TestSprintboardClaimAndComplete(t *testing.T) {
	claims := 0
	completes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tickets/T-7442-1/claim":
			claims++
		case "/api/v1/tickets/T-7442-1/complete":
			completes++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{
		BaseURL:   srv.URL,
		AgentName: "worker",
	}, nil)

	require.NoError(t, client.ClaimTicket(context.Background(), "T-7442-1"))
	require.NoError(t, client.CompleteTicket(context.Background(), "T-7442-1", "tests pass"))
	assert.Equal(t, 1, claims)
	assert.Equal(t, 1, completes)
}

func TestSprintboardSprintStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/sprints/v7400", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sprint_id":   "v7400",
			"total":       100,
			"completed":   42,
			"in_progress": 3,
		})
	}))
	defer srv.Close()

	client := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL}, nil)
	status, err := client.SprintStatus(context.Background(), "v7400")
	require.NoError(t, err)
	assert.Equal(t, "v7400", status["sprint_id"])
	assert.Equal(t, float64(42), status["completed"])
}
