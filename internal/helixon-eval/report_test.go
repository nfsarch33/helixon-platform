// report_test.go — TDD-style tests for Report.Aggregate and
// Report.Score. Pairs with registry_test.go so the four behaviour
// targets in the brief each have a regression test.
package helixoneval

import (
	"bytes"
	"strings"
	"testing"
)

func TestReport_Aggregate_EmptyRegistry(t *testing.T) {
	rep := Report{}
	rep.Aggregate(NewRegistry(), "v16129", 0.7)
	if rep.OverallScore != 0 {
		t.Fatalf("want overall 0, got %v", rep.OverallScore)
	}
	if rep.Pass {
		t.Fatalf("want Pass=false on empty registry")
	}
	if len(rep.ModelStats) != 0 {
		t.Fatalf("want empty ModelStats, got %v", rep.ModelStats)
	}
}

func TestReport_Aggregate_PerModelStatsCorrect(t *testing.T) {
	reg := NewRegistry()
	for _, m := range AllModels() {
		_ = reg.Upsert(Case{
			ID: "t::" + string(m), Task: "t", Model: m,
			Score: 0.8, Steps: 6, TerminationReason: "completed",
		})
	}
	rep := Report{}
	rep.Aggregate(reg, "v16129", 0.7)
	if len(rep.ModelStats) != len(AllModels()) {
		t.Fatalf("want %d models, got %d", len(AllModels()), len(rep.ModelStats))
	}
	for _, m := range AllModels() {
		s := rep.ModelStats[m]
		if s.Count != 1 || !approxEqual(s.MeanScore, 0.8, 1e-9) || s.MedianSteps != 6 || s.Completions != 1 {
			t.Fatalf("model %s stats wrong: %+v", m, s)
		}
	}
	if !approxEqual(rep.OverallScore, 0.8, 1e-9) {
		t.Fatalf("OverallScore want 0.8 got %v", rep.OverallScore)
	}
	if !rep.Pass {
		t.Fatalf("want Pass=true at threshold 0.7")
	}
}

func TestReport_Aggregate_FailsAtThreshold(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Upsert(Case{ID: "a", Model: ModelQwen37Plus, Score: 0.5, Steps: 4, TerminationReason: "completed"})
	rep := Report{}
	rep.Aggregate(reg, "v16129", 0.7)
	if rep.Pass {
		t.Fatalf("want Pass=false below 0.7")
	}
}

func TestReport_Score_ReturnsOverallScore(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Upsert(Case{ID: "a", Model: ModelQwen37Plus, Score: 0.85, Steps: 5, TerminationReason: "completed"})
	rep := Report{}
	got := rep.Score(reg, 0.7)
	if !approxEqual(got, 0.85, 1e-9) {
		t.Fatalf("Score: want 0.85 got %v", got)
	}
	if !rep.Pass {
		t.Fatalf("Score(0.7) should mark Pass=true")
	}
}

// approxEqual returns true if |a-b| < eps.
func approxEqual(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

func TestReport_Aggregate_GoldenSetPassesThreshold(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)
	n, err := runner.RunAll(AllModels(), GoldenCatalog())
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if n != 15 {
		t.Fatalf("want 15 cases (5 tasks * 3 models), got %d", n)
	}
	rep := Report{}
	rep.Aggregate(reg, "v16129", 0.7)
	if !rep.Pass {
		t.Fatalf("golden set must pass threshold 0.7, got %.3f", rep.OverallScore)
	}
	for _, m := range AllModels() {
		s := rep.ModelStats[m]
		if s.MeanScore < 0.7 {
			t.Fatalf("model %s mean %.3f below 0.7", m, s.MeanScore)
		}
	}
}

func TestReport_WriteText_ContainsHeaderAndTable(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Upsert(Case{
		ID: "demo::qwen3.7-plus", Task: "demo", Model: ModelQwen37Plus,
		Score: 0.83, Steps: 5, TerminationReason: "completed",
	})
	rep := Report{}
	rep.Aggregate(reg, "v16129", 0.7)
	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	s := buf.String()
	wantSubstrings := []string{
		"# HelixonEval Report",
		"## Per-model",
		"## Per-case",
		"qwen3.7-plus",
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Fatalf("WriteText output missing %q\n--- output ---\n%s", w, s)
		}
	}
}

func TestReport_CasesPreserveInsertionOrder(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Upsert(Case{ID: "c", Model: ModelQwen37Plus, Score: 0.7})
	_ = reg.Upsert(Case{ID: "a", Model: ModelQwen37Plus, Score: 0.8})
	_ = reg.Upsert(Case{ID: "b", Model: ModelQwen37Plus, Score: 0.9})
	rep := Report{}
	rep.Aggregate(reg, "v16129", 0.7)
	if rep.Cases[0].ID != "c" || rep.Cases[1].ID != "a" || rep.Cases[2].ID != "b" {
		t.Fatalf("order wrong: %v", rep.Cases)
	}
}

func TestReport_Aggregate_NilRegistryHandled(t *testing.T) {
	rep := Report{}
	rep.Aggregate(nil, "v16129", 0.7)
	if rep.Cases != nil || len(rep.ModelStats) != 0 {
		t.Fatalf("nil registry should produce empty report, got %+v", rep)
	}
}

// median helper exposed via package test.
func TestMedian_EmptyZero(t *testing.T) {
	if median(nil) != 0 || median([]int{}) != 0 {
		t.Fatalf("median(empty) must be 0")
	}
}

func TestMedian_OddCount(t *testing.T) {
	if median([]int{4, 1, 3}) != 3 {
		t.Fatalf("median([4,1,3]) want 3")
	}
}

func TestMedian_EvenRoundsUp(t *testing.T) {
	// Sprint 18 implementation uses the conventional sorted[n/2]
	// formula. For [1,2,3,4] sorted[4/2] = sorted[2] = 3. Document
	// this convention so future contributors don't trip over it.
	if median([]int{1, 2, 3, 4}) != 3 {
		t.Fatalf("median([1,2,3,4]) want 3 (sorted[2])")
	}
}
