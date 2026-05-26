package evalfw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ReportEvent is the NDJSON shape written after each suite run.
type ReportEvent struct {
	Timestamp  string             `json:"ts"`
	Suite      string             `json:"suite"`
	Verdict    Verdict            `json:"verdict"`
	Total      int                `json:"total"`
	Passed     int                `json:"passed"`
	Failed     int                `json:"failed"`
	Warned     int                `json:"warned"`
	DurationMS int64              `json:"duration_ms"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
}

// ReportWriter appends eval results as NDJSON events.
type ReportWriter struct {
	path string
}

// NewReportWriter creates a writer that appends to the given path.
// The parent directory is created if needed.
func NewReportWriter(path string) (*ReportWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &ReportWriter{path: path}, nil
}

// DefaultReportPath returns ~/logs/runx/eval-results.ndjson.
func DefaultReportPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "logs", "runx", "eval-results.ndjson")
}

// Write appends a SuiteResult as an NDJSON line.
func (w *ReportWriter) Write(result *SuiteResult) error {
	event := ReportEvent{
		Timestamp:  time.Now().Format(time.RFC3339),
		Suite:      result.Name,
		Verdict:    result.Verdict,
		Total:      result.TotalCases,
		Passed:     result.Passed,
		Failed:     result.Failed,
		Warned:     result.Warned,
		DurationMS: result.Duration.Milliseconds(),
		Metrics:    aggregateMetrics(result),
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

func aggregateMetrics(result *SuiteResult) map[string]float64 {
	if len(result.Cases) == 0 {
		return nil
	}
	agg := make(map[string]float64)
	counts := make(map[string]int)
	for _, c := range result.Cases {
		for k, v := range c.Metrics {
			agg[k] += v
			counts[k]++
		}
	}
	for k, count := range counts {
		if count > 0 {
			agg[k+"_avg"] = agg[k] / float64(count)
		}
	}
	return agg
}
