package fleet

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateDailyReportEmpty(t *testing.T) {
	start := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	report := GenerateDailyReport("agent-1", nil, start, end)
	assert.Equal(t, "agent-1", report.AgentID)
	assert.Equal(t, "2026-05-26", report.Date)
	assert.Equal(t, 0, report.Total)
	assert.Empty(t, report.Tasks)
}

func TestGenerateDailyReportMixedStatuses(t *testing.T) {
	start := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	startedAt := start.Add(1 * time.Hour)
	completedAt := startedAt.Add(30 * time.Second)
	failedAt := startedAt.Add(10 * time.Second)

	tasks := []TaskRecord{
		{
			ID:          "task-1",
			TicketID:    "T-1",
			Status:      TaskStatusCompleted,
			Attempts:    1,
			SubmittedAt: start.Add(30 * time.Minute),
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
		},
		{
			ID:          "task-2",
			Status:      TaskStatusCompleted,
			Attempts:    2,
			SubmittedAt: start.Add(2 * time.Hour),
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
		},
		{
			ID:          "task-3",
			Status:      TaskStatusFailed,
			Attempts:    3,
			Error:       "something broke",
			SubmittedAt: start.Add(3 * time.Hour),
			StartedAt:   &startedAt,
			CompletedAt: &failedAt,
		},
		{
			ID:          "task-4",
			Status:      TaskStatusTimedOut,
			Attempts:    1,
			Error:       "deadline exceeded",
			SubmittedAt: start.Add(4 * time.Hour),
			StartedAt:   &startedAt,
			CompletedAt: &failedAt,
		},
		// out of window
		{
			ID:          "task-yesterday",
			Status:      TaskStatusCompleted,
			SubmittedAt: start.Add(-1 * time.Hour),
			CompletedAt: func() *time.Time { t := start.Add(-30 * time.Minute); return &t }(),
		},
	}

	report := GenerateDailyReport("fleet-1", tasks, start, end)
	assert.Equal(t, "fleet-1", report.AgentID)
	assert.Equal(t, 4, report.Total)
	assert.Equal(t, 2, report.Completed)
	assert.Equal(t, 1, report.Failed)
	assert.Equal(t, 1, report.TimedOut)
	assert.Len(t, report.Tasks, 4)
	assert.Equal(t, 30*time.Second, report.AvgLatency)
}

func TestGenerateDailyReportSorted(t *testing.T) {
	start := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	tasks := []TaskRecord{
		{ID: "z-task", Status: TaskStatusCompleted, SubmittedAt: start.Add(1 * time.Hour)},
		{ID: "a-task", Status: TaskStatusCompleted, SubmittedAt: start.Add(2 * time.Hour)},
		{ID: "m-task", Status: TaskStatusCompleted, SubmittedAt: start.Add(3 * time.Hour)},
	}

	report := GenerateDailyReport("sorted", tasks, start, end)
	require.Len(t, report.Tasks, 3)
	assert.Equal(t, "a-task", report.Tasks[0].ID)
	assert.Equal(t, "m-task", report.Tasks[1].ID)
	assert.Equal(t, "z-task", report.Tasks[2].ID)
}

func TestFormatReport(t *testing.T) {
	start := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	report := DailyReport{
		AgentID:     "test-agent",
		Date:        "2026-05-26",
		Total:       3,
		Completed:   2,
		Failed:      1,
		AvgLatency:  500 * time.Millisecond,
		GeneratedAt: start,
		Tasks: []TaskSummary{
			{ID: "t-1", Status: TaskStatusCompleted, Attempts: 1, Duration: "500ms"},
			{ID: "t-2", Status: TaskStatusCompleted, Attempts: 1, Duration: "600ms", TicketID: "T-1"},
			{ID: "t-3", Status: TaskStatusFailed, Attempts: 3, Duration: "1s", Error: "broken"},
		},
	}

	text := FormatReport(report)
	assert.Contains(t, text, "Fleet Daily Report")
	assert.Contains(t, text, "test-agent")
	assert.Contains(t, text, "Total:       3")
	assert.Contains(t, text, "Completed:   2")
	assert.Contains(t, text, "Failed:      1")
	assert.Contains(t, text, "500ms")
	assert.Contains(t, text, "ticket=T-1")
	assert.Contains(t, text, "err=broken")
}

func TestFormatReportEmptyTasks(t *testing.T) {
	report := DailyReport{
		AgentID: "empty-agent",
		Date:    "2026-05-26",
	}

	text := FormatReport(report)
	assert.Contains(t, text, "empty-agent")
	assert.NotContains(t, text, "--- Tasks ---")
}

func TestInWindow(t *testing.T) {
	start := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	assert.True(t, inWindow(TaskRecord{SubmittedAt: start.Add(1 * time.Hour)}, start, end))
	assert.True(t, inWindow(TaskRecord{SubmittedAt: start}, start, end))
	assert.False(t, inWindow(TaskRecord{SubmittedAt: start.Add(-1 * time.Hour)}, start, end))
	assert.False(t, inWindow(TaskRecord{SubmittedAt: end}, start, end))

	completedInWindow := start.Add(12 * time.Hour)
	assert.True(t, inWindow(TaskRecord{
		SubmittedAt: start.Add(-1 * time.Hour),
		CompletedAt: &completedInWindow,
	}, start, end))
}

func TestTaskSummaryErrorTruncation(t *testing.T) {
	start := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	longErr := ""
	for i := 0; i < 200; i++ {
		longErr += "x"
	}

	tasks := []TaskRecord{
		{
			ID:          "long-err",
			Status:      TaskStatusFailed,
			Error:       longErr,
			SubmittedAt: start.Add(1 * time.Hour),
		},
	}

	report := GenerateDailyReport("trunc", tasks, start, end)
	text := FormatReport(report)
	assert.Contains(t, text, "...")
}
