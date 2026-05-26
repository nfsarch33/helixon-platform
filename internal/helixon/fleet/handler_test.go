package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockExecutor struct {
	mu        sync.Mutex
	calls     []string
	result    string
	err       error
	delay     time.Duration
	failN     int
	callCount int
}

func (m *mockExecutor) ExecuteTask(_ context.Context, taskID, prompt string) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, taskID+":"+prompt)
	m.callCount++
	n := m.callCount
	m.mu.Unlock()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.failN > 0 && n <= m.failN {
		return "", fmt.Errorf("transient failure attempt %d", n)
	}
	if m.err != nil {
		return "", m.err
	}
	return m.result, nil
}

type mockClaimer struct {
	mu        sync.Mutex
	claimed   []string
	completed map[string]string
	claimErr  error
}

func newMockClaimer() *mockClaimer {
	return &mockClaimer{completed: make(map[string]string)}
}

func (m *mockClaimer) ClaimTicket(_ context.Context, ticketID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return m.claimErr
	}
	m.claimed = append(m.claimed, ticketID)
	return nil
}

func (m *mockClaimer) CompleteTicket(_ context.Context, ticketID, evidence string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed[ticketID] = evidence
	return nil
}

func TestHandlerSubmitAndComplete(t *testing.T) {
	exec := &mockExecutor{result: "task done"}
	claimer := newMockClaimer()
	h := NewHandler(exec, claimer, HandlerConfig{MaxConcurrent: 2})

	ctx := context.Background()
	taskID, err := h.Submit(ctx, TaskSubmission{
		AgentName: "test-agent",
		Prompt:    "do something",
		TicketID:  "T-100",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, taskID)

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask(taskID)
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)

	rec, ok := h.GetTask(taskID)
	require.True(t, ok)
	assert.Equal(t, TaskStatusCompleted, rec.Status)
	assert.Equal(t, "task done", rec.Result)
	assert.Equal(t, 1, rec.Attempts)
	assert.NotNil(t, rec.StartedAt)
	assert.NotNil(t, rec.CompletedAt)
	assert.Greater(t, rec.Duration(), time.Duration(0))

	claimer.mu.Lock()
	assert.Contains(t, claimer.claimed, "T-100")
	assert.Contains(t, claimer.completed, "T-100")
	claimer.mu.Unlock()
}

func TestHandlerSubmitWithCustomID(t *testing.T) {
	exec := &mockExecutor{result: "ok"}
	h := NewHandler(exec, nil, HandlerConfig{})

	taskID, err := h.Submit(context.Background(), TaskSubmission{
		TaskID:    "custom-id-42",
		AgentName: "agent",
		Prompt:    "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "custom-id-42", taskID)

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask("custom-id-42")
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
}

func TestHandlerEmptyPromptReject(t *testing.T) {
	h := NewHandler(&mockExecutor{}, nil, HandlerConfig{})
	_, err := h.Submit(context.Background(), TaskSubmission{AgentName: "a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is required")
}

func TestHandlerRetryOnTransientFailure(t *testing.T) {
	exec := &mockExecutor{
		result: "recovered",
		failN:  2,
	}
	h := NewHandler(exec, nil, HandlerConfig{MaxRetries: 3})

	taskID, err := h.Submit(context.Background(), TaskSubmission{
		AgentName: "retry-agent",
		Prompt:    "retry task",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask(taskID)
		return ok && rec.Status == TaskStatusCompleted
	}, 10*time.Second, 20*time.Millisecond)

	rec, _ := h.GetTask(taskID)
	assert.Equal(t, 3, rec.Attempts)
	assert.Equal(t, "recovered", rec.Result)
}

func TestHandlerPermanentFailure(t *testing.T) {
	exec := &mockExecutor{err: errors.New("permanent failure")}
	claimer := newMockClaimer()
	h := NewHandler(exec, claimer, HandlerConfig{MaxRetries: 1})

	taskID, err := h.Submit(context.Background(), TaskSubmission{
		AgentName: "fail-agent",
		Prompt:    "will fail",
		TicketID:  "T-FAIL",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask(taskID)
		return ok && (rec.Status == TaskStatusFailed || rec.Status == TaskStatusTimedOut)
	}, 10*time.Second, 20*time.Millisecond)

	rec, _ := h.GetTask(taskID)
	assert.Equal(t, TaskStatusFailed, rec.Status)
	assert.Contains(t, rec.Error, "permanent failure")

	claimer.mu.Lock()
	assert.Contains(t, claimer.completed, "T-FAIL")
	claimer.mu.Unlock()
}

func TestHandlerConcurrencyLimit(t *testing.T) {
	var running atomic.Int32
	var maxRunning atomic.Int32

	exec := &mockExecutor{
		result: "ok",
		delay:  50 * time.Millisecond,
	}
	origExec := exec.ExecuteTask
	_ = origExec

	slowExec := &countingExecutor{
		result:     "ok",
		delay:      50 * time.Millisecond,
		running:    &running,
		maxRunning: &maxRunning,
	}

	h := NewHandler(slowExec, nil, HandlerConfig{MaxConcurrent: 2})
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		_, err := h.Submit(ctx, TaskSubmission{
			AgentName: "concurrent",
			Prompt:    fmt.Sprintf("task %d", i),
		})
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool {
		tasks := h.ListTasks()
		for _, t := range tasks {
			if t.Status != TaskStatusCompleted {
				return false
			}
		}
		return len(tasks) == 6
	}, 10*time.Second, 20*time.Millisecond)

	assert.LessOrEqual(t, int(maxRunning.Load()), 2)
}

type countingExecutor struct {
	result     string
	delay      time.Duration
	running    *atomic.Int32
	maxRunning *atomic.Int32
}

func (e *countingExecutor) ExecuteTask(_ context.Context, _, _ string) (string, error) {
	cur := e.running.Add(1)
	for {
		old := e.maxRunning.Load()
		if cur <= old || e.maxRunning.CompareAndSwap(old, cur) {
			break
		}
	}
	defer e.running.Add(-1)

	time.Sleep(e.delay)
	return e.result, nil
}

func TestHandlerOnTaskCompleteCallback(t *testing.T) {
	exec := &mockExecutor{result: "done"}
	h := NewHandler(exec, nil, HandlerConfig{})

	var completed []TaskRecord
	var mu sync.Mutex
	h.OnTaskComplete(func(rec TaskRecord) {
		mu.Lock()
		completed = append(completed, rec)
		mu.Unlock()
	})

	_, err := h.Submit(context.Background(), TaskSubmission{
		AgentName: "cb-agent",
		Prompt:    "callback test",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(completed) > 0
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	assert.Equal(t, TaskStatusCompleted, completed[0].Status)
	mu.Unlock()
}

func TestHandlerListTasks(t *testing.T) {
	exec := &mockExecutor{result: "ok"}
	h := NewHandler(exec, nil, HandlerConfig{})

	for i := 0; i < 3; i++ {
		_, err := h.Submit(context.Background(), TaskSubmission{
			AgentName: "list-agent",
			Prompt:    fmt.Sprintf("task %d", i),
		})
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool {
		return len(h.ListTasks()) == 3
	}, 5*time.Second, 10*time.Millisecond)

	tasks := h.ListTasks()
	assert.Len(t, tasks, 3)
}

func TestHandlerActiveCount(t *testing.T) {
	exec := &mockExecutor{result: "ok", delay: 100 * time.Millisecond}
	h := NewHandler(exec, nil, HandlerConfig{MaxConcurrent: 4})

	for i := 0; i < 3; i++ {
		_, _ = h.Submit(context.Background(), TaskSubmission{
			AgentName: "active",
			Prompt:    fmt.Sprintf("task %d", i),
		})
	}

	time.Sleep(20 * time.Millisecond)
	assert.LessOrEqual(t, h.ActiveCount(), 4)
}

func TestHandlerNilClaimer(t *testing.T) {
	exec := &mockExecutor{result: "ok"}
	h := NewHandler(exec, nil, HandlerConfig{})

	taskID, err := h.Submit(context.Background(), TaskSubmission{
		AgentName: "no-claimer",
		Prompt:    "no ticket",
		TicketID:  "T-999",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask(taskID)
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
}

func TestHandlerClaimFailureNonFatal(t *testing.T) {
	exec := &mockExecutor{result: "ok"}
	claimer := newMockClaimer()
	claimer.claimErr = errors.New("sprintboard down")
	h := NewHandler(exec, claimer, HandlerConfig{})

	taskID, err := h.Submit(context.Background(), TaskSubmission{
		AgentName: "claim-fail",
		Prompt:    "proceeds despite claim failure",
		TicketID:  "T-NOPE",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask(taskID)
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
}

func TestHandlerHTTPSubmit(t *testing.T) {
	exec := &mockExecutor{result: "http-result"}
	h := NewHandler(exec, nil, HandlerConfig{})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := `{"agent_name":"http-agent","prompt":"do it"}`
	resp, err := http.Post(srv.URL+"/api/v1/fleet/tasks", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotEmpty(t, result["task_id"])
	assert.Equal(t, "pending", result["status"])
}

func TestHandlerHTTPGetTask(t *testing.T) {
	exec := &mockExecutor{result: "found"}
	h := NewHandler(exec, nil, HandlerConfig{})

	taskID, _ := h.Submit(context.Background(), TaskSubmission{
		AgentName: "get-agent",
		Prompt:    "get test",
	})

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask(taskID)
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/fleet/tasks/" + taskID)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var rec TaskRecord
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rec))
	assert.Equal(t, TaskStatusCompleted, rec.Status)
	assert.Equal(t, "found", rec.Result)
}

func TestHandlerHTTPGetTaskNotFound(t *testing.T) {
	h := NewHandler(&mockExecutor{}, nil, HandlerConfig{})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/fleet/tasks/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandlerHTTPListTasks(t *testing.T) {
	exec := &mockExecutor{result: "ok"}
	h := NewHandler(exec, nil, HandlerConfig{})

	_, _ = h.Submit(context.Background(), TaskSubmission{AgentName: "a", Prompt: "p1"})
	_, _ = h.Submit(context.Background(), TaskSubmission{AgentName: "b", Prompt: "p2"})

	require.Eventually(t, func() bool {
		tasks := h.ListTasks()
		done := 0
		for _, t := range tasks {
			if t.Status == TaskStatusCompleted {
				done++
			}
		}
		return done == 2
	}, 5*time.Second, 10*time.Millisecond)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/fleet/tasks")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var tasks []TaskRecord
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tasks))
	assert.Len(t, tasks, 2)
}

func TestHandlerHTTPSubmitBadBody(t *testing.T) {
	h := NewHandler(&mockExecutor{}, nil, HandlerConfig{})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/fleet/tasks", "application/json", strings.NewReader("{invalid"))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandlerHTTPSubmitNoPrompt(t *testing.T) {
	h := NewHandler(&mockExecutor{}, nil, HandlerConfig{})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/fleet/tasks", "application/json",
		strings.NewReader(`{"agent_name":"a"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestTaskRecordDuration(t *testing.T) {
	now := time.Now()
	later := now.Add(5 * time.Second)

	rec := TaskRecord{StartedAt: &now, CompletedAt: &later}
	assert.Equal(t, 5*time.Second, rec.Duration())

	rec2 := TaskRecord{}
	assert.Equal(t, time.Duration(0), rec2.Duration())

	started := time.Now().Add(-1 * time.Second)
	rec3 := TaskRecord{StartedAt: &started}
	assert.Greater(t, rec3.Duration(), 900*time.Millisecond)
}

func TestHandlerDefaultConfig(t *testing.T) {
	cfg := HandlerConfig{}
	cfg = cfg.withDefaults()
	assert.Equal(t, 4, cfg.MaxConcurrent)
	assert.Equal(t, 10*time.Minute, cfg.DefaultTimeout)
	assert.Equal(t, 2, cfg.MaxRetries)
	assert.NotNil(t, cfg.Logger)
}

func TestHandlerCustomTimeout(t *testing.T) {
	exec := &mockExecutor{result: "ok", delay: 10 * time.Millisecond}
	h := NewHandler(exec, nil, HandlerConfig{DefaultTimeout: 5 * time.Second})

	taskID, err := h.Submit(context.Background(), TaskSubmission{
		AgentName:   "timeout-agent",
		Prompt:      "quick",
		TimeoutSecs: 30,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, ok := h.GetTask(taskID)
		return ok && rec.Status == TaskStatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
}
