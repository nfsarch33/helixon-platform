package fleet

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DailyReport summarises a day's task execution for fleet reporting.
type DailyReport struct {
	AgentID     string        `json:"agent_id"`
	Date        string        `json:"date"`
	Total       int           `json:"total"`
	Completed   int           `json:"completed"`
	Failed      int           `json:"failed"`
	TimedOut    int           `json:"timed_out"`
	AvgLatency  time.Duration `json:"avg_latency_ms"`
	Tasks       []TaskSummary `json:"tasks"`
	GeneratedAt time.Time     `json:"generated_at"`
}

// TaskSummary is a compact view of a single task for the daily report.
type TaskSummary struct {
	ID       string     `json:"id"`
	TicketID string     `json:"ticket_id,omitempty"`
	Status   TaskStatus `json:"status"`
	Attempts int        `json:"attempts"`
	Duration string     `json:"duration"`
	Error    string     `json:"error,omitempty"`
}

// GenerateDailyReport creates a daily report from the handler's task log.
// It filters tasks completed within the given date window.
func GenerateDailyReport(agentID string, tasks []TaskRecord, start, end time.Time) DailyReport {
	report := DailyReport{
		AgentID:     agentID,
		Date:        start.Format("2006-01-02"),
		GeneratedAt: time.Now().UTC(),
	}

	var totalDuration time.Duration
	var completedCount int

	for _, t := range tasks {
		if !inWindow(t, start, end) {
			continue
		}

		report.Total++
		summary := TaskSummary{
			ID:       t.ID,
			TicketID: t.TicketID,
			Status:   t.Status,
			Attempts: t.Attempts,
			Duration: t.Duration().Round(time.Millisecond).String(),
			Error:    t.Error,
		}

		switch t.Status {
		case TaskStatusPending, TaskStatusClaimed, TaskStatusRunning:
			// in-flight; not yet counted in any outcome bucket
		case TaskStatusCompleted:
			report.Completed++
			completedCount++
			totalDuration += t.Duration()
		case TaskStatusFailed:
			report.Failed++
		case TaskStatusTimedOut:
			report.TimedOut++
		}

		report.Tasks = append(report.Tasks, summary)
	}

	if completedCount > 0 {
		report.AvgLatency = totalDuration / time.Duration(completedCount)
	}

	sort.Slice(report.Tasks, func(i, j int) bool {
		return report.Tasks[i].ID < report.Tasks[j].ID
	})

	return report
}

// FormatReport renders the daily report as a human-readable text block.
func FormatReport(r DailyReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Fleet Daily Report ===\n")
	fmt.Fprintf(&b, "Agent:       %s\n", r.AgentID)
	fmt.Fprintf(&b, "Date:        %s\n", r.Date)
	fmt.Fprintf(&b, "Total:       %d\n", r.Total)
	fmt.Fprintf(&b, "Completed:   %d\n", r.Completed)
	fmt.Fprintf(&b, "Failed:      %d\n", r.Failed)
	fmt.Fprintf(&b, "Timed Out:   %d\n", r.TimedOut)
	fmt.Fprintf(&b, "Avg Latency: %s\n", r.AvgLatency.Round(time.Millisecond))

	if len(r.Tasks) > 0 {
		fmt.Fprintf(&b, "\n--- Tasks ---\n")
		for _, t := range r.Tasks {
			line := fmt.Sprintf("  %s  %s  attempts=%d  %s", t.ID, t.Status, t.Attempts, t.Duration)
			if t.TicketID != "" {
				line += fmt.Sprintf("  ticket=%s", t.TicketID)
			}
			if t.Error != "" {
				errSnippet := t.Error
				if len(errSnippet) > 80 {
					errSnippet = errSnippet[:80] + "..."
				}
				line += fmt.Sprintf("  err=%s", errSnippet)
			}
			fmt.Fprintln(&b, line)
		}
	}

	fmt.Fprintf(&b, "\nGenerated: %s\n", r.GeneratedAt.Format(time.RFC3339))
	return b.String()
}

func inWindow(t TaskRecord, start, end time.Time) bool {
	ts := t.SubmittedAt
	if t.CompletedAt != nil {
		ts = *t.CompletedAt
	}
	return !ts.Before(start) && ts.Before(end)
}
