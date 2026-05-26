package evalfw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReportWriter_WritesNDJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "eval-results.ndjson")

	w, err := NewReportWriter(path)
	if err != nil {
		t.Fatalf("NewReportWriter: %v", err)
	}

	result := &SuiteResult{
		Name:       "smoke",
		TotalCases: 2,
		Passed:     1,
		Failed:     1,
		Verdict:    VerdictFail,
		Duration:   150 * time.Millisecond,
		Cases: []CaseResult{
			{Name: "fast", Verdict: VerdictPass, Metrics: map[string]float64{"latency_ms": 10}},
			{Name: "slow", Verdict: VerdictFail, Metrics: map[string]float64{"latency_ms": 200}},
		},
	}

	if err := w.Write(result); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var event ReportEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if event.Suite != "smoke" {
		t.Fatalf("Suite = %q, want smoke", event.Suite)
	}
	if event.Verdict != VerdictFail {
		t.Fatalf("Verdict = %q, want FAIL", event.Verdict)
	}
	if event.Total != 2 {
		t.Fatalf("Total = %d, want 2", event.Total)
	}
	if event.Passed != 1 {
		t.Fatalf("Passed = %d, want 1", event.Passed)
	}
	if event.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", event.Failed)
	}
	if event.DurationMS != 150 {
		t.Fatalf("DurationMS = %d, want 150", event.DurationMS)
	}
	if avg, ok := event.Metrics["latency_ms_avg"]; !ok || avg != 105 {
		t.Fatalf("latency_ms_avg = %v (ok=%v), want 105", avg, ok)
	}
}

func TestReportWriter_AppendsMultipleRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "eval-results.ndjson")

	w, err := NewReportWriter(path)
	if err != nil {
		t.Fatalf("NewReportWriter: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := w.Write(&SuiteResult{Name: "run", Verdict: VerdictPass}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Fatalf("expected 3 NDJSON lines, got %d", lines)
	}
}

func TestDefaultReportPath_NotEmpty(t *testing.T) {
	t.Parallel()
	p := DefaultReportPath()
	if p == "" {
		t.Fatal("DefaultReportPath returned empty")
	}
	if filepath.Ext(p) != ".ndjson" {
		t.Fatalf("expected .ndjson extension, got %q", filepath.Ext(p))
	}
}
