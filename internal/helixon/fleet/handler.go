package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TaskStatus tracks the lifecycle of a delegated task.
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusClaimed   TaskStatus = "claimed"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusTimedOut  TaskStatus = "timed_out"
)

// TaskSubmission is the inbound payload for a new task from an external agent.
type TaskSubmission struct {
	TaskID      string         `json:"task_id,omitempty"`
	AgentName   string         `json:"agent_name"`
	Prompt      string         `json:"prompt"`
	TicketID    string         `json:"ticket_id,omitempty"`
	Priority    int            `json:"priority,omitempty"`
	TimeoutSecs int            `json:"timeout_secs,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskRecord is the internal representation of a submitted task.
type TaskRecord struct {
	ID          string         `json:"id"`
	AgentName   string         `json:"agent_name"`
	Prompt      string         `json:"prompt"`
	TicketID    string         `json:"ticket_id,omitempty"`
	Priority    int            `json:"priority"`
	Status      TaskStatus     `json:"status"`
	Result      string         `json:"result,omitempty"`
	Error       string         `json:"error,omitempty"`
	SubmittedAt time.Time      `json:"submitted_at"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	Attempts    int            `json:"attempts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Duration returns the elapsed execution time, or zero if not started.
func (r *TaskRecord) Duration() time.Duration {
	if r.StartedAt == nil {
		return 0
	}
	end := time.Now()
	if r.CompletedAt != nil {
		end = *r.CompletedAt
	}
	return end.Sub(*r.StartedAt)
}

// TaskExecutor processes a task prompt and returns a result.
type TaskExecutor interface {
	ExecuteTask(ctx context.Context, taskID, prompt string) (string, error)
}

// SprintboardClaimer claims and completes tickets on SprintBoard.
type SprintboardClaimer interface {
	ClaimTicket(ctx context.Context, ticketID string) error
	CompleteTicket(ctx context.Context, ticketID, evidence string) error
}

// HandlerConfig configures the fleet task handler.
type HandlerConfig struct {
	MaxConcurrent  int
	DefaultTimeout time.Duration
	MaxRetries     int
	Logger         *slog.Logger
}

func (c HandlerConfig) withDefaults() HandlerConfig {
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 4
	}
	if c.DefaultTimeout <= 0 {
		c.DefaultTimeout = 10 * time.Minute
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 2
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Handler receives tasks via the A2A protocol, delegates them to the
// agent executor, and reports results back. It manages concurrency
// limits and retry logic.
type Handler struct {
	cfg       HandlerConfig
	executor  TaskExecutor
	claimer   SprintboardClaimer
	logger    *slog.Logger
	sem       chan struct{}
	mu        sync.RWMutex
	tasks     map[string]*TaskRecord
	listeners []func(TaskRecord)
}

// NewHandler creates a fleet task handler.
func NewHandler(executor TaskExecutor, claimer SprintboardClaimer, cfg HandlerConfig) *Handler {
	cfg = cfg.withDefaults()
	return &Handler{
		cfg:      cfg,
		executor: executor,
		claimer:  claimer,
		logger:   cfg.Logger.With(slog.String("component", "helixon.fleet.handler")),
		sem:      make(chan struct{}, cfg.MaxConcurrent),
		tasks:    make(map[string]*TaskRecord),
	}
}

// OnTaskComplete registers a callback invoked after each task completes or fails.
func (h *Handler) OnTaskComplete(fn func(TaskRecord)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listeners = append(h.listeners, fn)
}

// Submit accepts a new task and processes it asynchronously.
// Returns the assigned task ID immediately.
func (h *Handler) Submit(ctx context.Context, sub TaskSubmission) (string, error) {
	if sub.Prompt == "" {
		return "", fmt.Errorf("fleet: task prompt is required")
	}

	taskID := sub.TaskID
	if taskID == "" {
		taskID = uuid.New().String()
	}

	timeout := h.cfg.DefaultTimeout
	if sub.TimeoutSecs > 0 {
		timeout = time.Duration(sub.TimeoutSecs) * time.Second
	}

	record := &TaskRecord{
		ID:          taskID,
		AgentName:   sub.AgentName,
		Prompt:      sub.Prompt,
		TicketID:    sub.TicketID,
		Priority:    sub.Priority,
		Status:      TaskStatusPending,
		SubmittedAt: time.Now().UTC(),
		Metadata:    sub.Metadata,
	}

	h.mu.Lock()
	h.tasks[taskID] = record
	h.mu.Unlock()

	h.logger.Info("task submitted",
		slog.String("task_id", taskID),
		slog.String("agent", sub.AgentName),
		slog.String("ticket", sub.TicketID),
	)

	go h.processTask(context.WithoutCancel(ctx), record, timeout)

	return taskID, nil
}

// GetTask returns the current state of a task.
func (h *Handler) GetTask(taskID string) (TaskRecord, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rec, ok := h.tasks[taskID]
	if !ok {
		return TaskRecord{}, false
	}
	return *rec, true
}

// ListTasks returns all task records.
func (h *Handler) ListTasks() []TaskRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]TaskRecord, 0, len(h.tasks))
	for _, rec := range h.tasks {
		out = append(out, *rec)
	}
	return out
}

// ActiveCount returns the number of currently executing tasks.
func (h *Handler) ActiveCount() int {
	return len(h.sem)
}

func (h *Handler) processTask(ctx context.Context, record *TaskRecord, timeout time.Duration) {
	select {
	case h.sem <- struct{}{}:
	case <-ctx.Done():
		h.updateStatus(record, TaskStatusFailed, "", "context cancelled waiting for semaphore")
		return
	}
	defer func() { <-h.sem }()

	if record.TicketID != "" && h.claimer != nil {
		if err := h.claimer.ClaimTicket(ctx, record.TicketID); err != nil {
			h.logger.Warn("failed to claim ticket (continuing execution)",
				slog.String("task_id", record.ID),
				slog.String("ticket", record.TicketID),
				slog.String("error", err.Error()),
			)
		}
	}

	now := time.Now().UTC()
	h.mu.Lock()
	record.Status = TaskStatusRunning
	record.StartedAt = &now
	h.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt <= h.cfg.MaxRetries; attempt++ {
		h.mu.Lock()
		record.Attempts = attempt + 1
		h.mu.Unlock()
		execCtx, cancel := context.WithTimeout(ctx, timeout)

		result, err := h.executor.ExecuteTask(execCtx, record.ID, record.Prompt)
		cancel()

		if err == nil {
			h.completeTask(ctx, record, result)
			return
		}

		lastErr = err
		h.logger.Warn("task execution failed",
			slog.String("task_id", record.ID),
			slog.Int("attempt", attempt+1),
			slog.Int("max_retries", h.cfg.MaxRetries),
			slog.String("error", err.Error()),
		)

		if attempt < h.cfg.MaxRetries {
			backoff := time.Duration(1<<uint(attempt)) * time.Second //nolint:gosec // G115 int conversion bounded by upstream length check
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				h.updateStatus(record, TaskStatusFailed, "", "context cancelled during retry backoff")
				return
			}
		}
	}

	if ctx.Err() != nil {
		h.updateStatus(record, TaskStatusTimedOut, "", lastErr.Error())
	} else {
		h.updateStatus(record, TaskStatusFailed, "", lastErr.Error())
	}

	if record.TicketID != "" && h.claimer != nil {
		evidence := fmt.Sprintf("failed after %d attempts: %s", record.Attempts, lastErr.Error())
		_ = h.claimer.CompleteTicket(ctx, record.TicketID, evidence)
	}
}

func (h *Handler) completeTask(ctx context.Context, record *TaskRecord, result string) {
	h.updateStatus(record, TaskStatusCompleted, result, "")

	if record.TicketID != "" && h.claimer != nil {
		evidence := result
		if len(evidence) > 500 {
			evidence = evidence[:500] + "..."
		}
		if err := h.claimer.CompleteTicket(ctx, record.TicketID, evidence); err != nil {
			h.logger.Warn("failed to complete ticket",
				slog.String("task_id", record.ID),
				slog.String("ticket", record.TicketID),
				slog.String("error", err.Error()),
			)
		}
	}

	h.logger.Info("task completed",
		slog.String("task_id", record.ID),
		slog.Duration("elapsed", record.Duration()),
		slog.Int("attempts", record.Attempts),
	)
}

func (h *Handler) updateStatus(record *TaskRecord, status TaskStatus, result, errMsg string) {
	now := time.Now().UTC()
	h.mu.Lock()
	record.Status = status
	record.Result = result
	record.Error = errMsg
	if status == TaskStatusCompleted || status == TaskStatusFailed || status == TaskStatusTimedOut {
		record.CompletedAt = &now
	}
	snapshot := *record
	listeners := make([]func(TaskRecord), len(h.listeners))
	copy(listeners, h.listeners)
	h.mu.Unlock()

	for _, fn := range listeners {
		fn(snapshot)
	}
}

// RegisterRoutes mounts the A2A task submission HTTP endpoints on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/fleet/tasks", h.submitHTTP)
	mux.HandleFunc("GET /api/v1/fleet/tasks/{id}", h.getTaskHTTP)
	mux.HandleFunc("GET /api/v1/fleet/tasks", h.listTasksHTTP)
}

func (h *Handler) submitHTTP(w http.ResponseWriter, r *http.Request) {
	var sub TaskSubmission
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		writeFleetJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	taskID, err := h.Submit(r.Context(), sub)
	if err != nil {
		writeFleetJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeFleetJSON(w, http.StatusAccepted, map[string]string{"task_id": taskID, "status": "pending"})
}

func (h *Handler) getTaskHTTP(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	record, ok := h.GetTask(taskID)
	if !ok {
		writeFleetJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	writeFleetJSON(w, http.StatusOK, record)
}

func (h *Handler) listTasksHTTP(w http.ResponseWriter, _ *http.Request) {
	tasks := h.ListTasks()
	writeFleetJSON(w, http.StatusOK, tasks)
}

func writeFleetJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
