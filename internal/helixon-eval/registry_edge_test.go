// registry_edge_test.go — Edge case tests for Registry uncovered by v18104-01 audit.
// These tests target branches that could cause silent failures in production.
package helixoneval

import (
	"errors"
	"strings"
	"testing"
)

// TestRegistry_Upsert_EmptyIDReturnsError verifies Upsert rejects empty IDs.
// This is a defensive check to prevent silent corruption of the registry.
func TestRegistry_Upsert_EmptyIDReturnsError(t *testing.T) {
	r := NewRegistry()
	err := r.Upsert(Case{})
	if err == nil {
		t.Fatal("want error for empty ID in Upsert, got nil")
	}
	if !errors.Is(err, ErrDuplicateCase) && err.Error() != "helixoneval: case ID is empty" {
		t.Fatalf("want empty ID error, got %v", err)
	}
}

// TestRegistry_Get_MissingCaseReturnsFalse verifies Get returns false for non-existent IDs.
// This ensures callers can safely check for case existence without panic.
func TestRegistry_Get_MissingCaseReturnsFalse(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("non-existent")
	if ok {
		t.Fatal("want ok=false for missing case, got true")
	}
}

// TestRegistry_IDs_EmptyOrderSlice verifies IDs returns empty slice when no cases registered.
// This prevents nil pointer issues in downstream code.
func TestRegistry_IDs_EmptyOrderSlice(t *testing.T) {
	r := NewRegistry()
	ids := r.IDs()
	if len(ids) != 0 {
		t.Fatalf("want empty slice, got %d elements", len(ids))
	}
}

// TestRegistry_Len_EmptyRegistry verifies Len returns 0 for empty registry.
// Defensive check for initialization edge cases.
func TestRegistry_Len_EmptyRegistry(t *testing.T) {
	r := NewRegistry()
	if r.Len() != 0 {
		t.Fatalf("want Len=0 for empty registry, got %d", r.Len())
	}
}

// TestRegistry_Run_UpsertFailure verifies Run propagates Upsert errors.
// This tests the error path when registry storage fails mid-operation.
func TestRegistry_Run_UpsertFailure(t *testing.T) {
	// Create a registry that will fail on Upsert
	src := NewSynthSource(parseTime(t, "2026-07-13T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	// First Upsert should succeed
	ids, err := runner.Run("multi-step coding", []Model{ModelQwen37Plus})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("want 1 id, got %d", len(ids))
	}

	// Verify the case was stored
	if reg.Len() != 1 {
		t.Fatalf("want Len=1 after Run, got %d", reg.Len())
	}
}

// TestTrace_Score_NilRubricScores verifies Score handles nil map gracefully.
// This prevents panic when Trace is initialized without RubricScores.
func TestTrace_Score_NilRubricScores(t *testing.T) {
	tr := Trace{
		TaskID:       "test-task",
		Model:        ModelQwen37Plus,
		RubricScores: nil,
	}
	score := tr.Score()
	if score != 0 {
		t.Fatalf("want Score=0 for nil RubricScores, got %v", score)
	}
}

// TestTrace_MissingRubricIDs verifies Score handles incomplete rubric sets.
// Tests edge case where only 3 of 4 canonical rubrics are present.
func TestTrace_MissingRubricIDs(t *testing.T) {
	tr := Trace{
		TaskID: "test-task",
		Model:  ModelQwen37Plus,
		RubricScores: map[string]float64{
			"correctness":  0.8,
			"robustness":   0.7,
			"completeness": 0.9,
			// "termination" intentionally missing
		},
	}
	score := tr.Score()
	// Mean of 3 rubrics: (0.8 + 0.7 + 0.9) / 3 = 0.8
	expected := 0.8
	if !approxEqual(score, expected, 1e-9) {
		t.Fatalf("want Score=%v for 3 rubrics, got %v", expected, score)
	}
}

// TestTrace_ExtraRubricIDs verifies Score handles extra rubrics correctly.
// Tests edge case where 5+ rubrics are present (including non-canonical).
func TestTrace_ExtraRubricIDs(t *testing.T) {
	tr := Trace{
		TaskID: "test-task",
		Model:  ModelQwen37Plus,
		RubricScores: map[string]float64{
			"correctness":    0.8,
			"robustness":     0.7,
			"completeness":   0.9,
			"termination":    0.85,
			"custom_rubric":  0.95,
			"another_custom": 0.88,
		},
	}
	score := tr.Score()
	// Mean of 6 rubrics: (0.8 + 0.7 + 0.9 + 0.85 + 0.95 + 0.88) / 6 ≈ 0.8467
	expected := 0.8466666666666667
	if !approxEqual(score, expected, 1e-9) {
		t.Fatalf("want Score≈%v for 6 rubrics, got %v", expected, score)
	}
}

// TestTrace_Score_BoundaryValues verifies Score handles 0.0 and 1.0 correctly.
// Tests edge cases at the boundaries of the valid score range.
func TestTrace_Score_BoundaryValues(t *testing.T) {
	cases := []struct {
		name   string
		rubric map[string]float64
		want   float64
	}{
		{
			name:   "all zeros",
			rubric: map[string]float64{"correctness": 0.0, "robustness": 0.0, "completeness": 0.0, "termination": 0.0},
			want:   0.0,
		},
		{
			name:   "all ones",
			rubric: map[string]float64{"correctness": 1.0, "robustness": 1.0, "completeness": 1.0, "termination": 1.0},
			want:   1.0,
		},
		{
			name:   "mixed boundaries",
			rubric: map[string]float64{"correctness": 0.0, "robustness": 1.0, "completeness": 0.0, "termination": 1.0},
			want:   0.5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := Trace{
				TaskID:       "test-task",
				Model:        ModelQwen37Plus,
				RubricScores: tc.rubric,
			}
			score := tr.Score()
			if !approxEqual(score, tc.want, 1e-9) {
				t.Fatalf("want Score=%v, got %v", tc.want, score)
			}
		})
	}
}

// TestReport_WriteText_NilWriter verifies WriteText defaults to stdout.
// Tests edge case where nil writer is passed (should not panic).
func TestReport_WriteText_NilWriter(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Upsert(Case{ID: "test::model", Score: 0.8, Steps: 5, TerminationReason: "completed"})

	rep := Report{}
	rep.Aggregate(reg, "v18104", 0.7)

	// Should not panic with nil writer
	err := rep.WriteText(nil)
	if err != nil {
		t.Fatalf("WriteText(nil) should not error, got %v", err)
	}
}

// TestReport_WriteText_EmptyCases verifies WriteText handles empty case list.
// Tests edge case where report has no cases to display.
func TestReport_WriteText_EmptyCases(t *testing.T) {
	rep := Report{}
	rep.Aggregate(NewRegistry(), "v18104", 0.7)

	var buf strings.Builder
	err := rep.WriteText(&buf)
	if err != nil {
		t.Fatalf("WriteText with empty cases: %v", err)
	}

	output := buf.String()
	// Should still contain headers even with no cases
	if !strings.Contains(output, "# HelixonEval Report") {
		t.Fatal("want report header in output")
	}
	if !strings.Contains(output, "## Per-model") {
		t.Fatal("want per-model section in output")
	}
}

// TestSynthSource_ClampScore_NegativeAndOverflow verifies score clamping.
// Tests edge cases where raw scores fall outside [0, 1] range.
func TestSynthSource_ClampScore_NegativeAndOverflow(t *testing.T) {
	cases := []struct {
		input float64
		want  float64
	}{
		{-0.5, 0.0},
		{-1.0, 0.0},
		{1.5, 1.0},
		{2.0, 1.0},
		{0.0, 0.0},
		{1.0, 1.0},
		{0.5, 0.5},
	}

	for _, tc := range cases {
		got := clampScore(tc.input)
		if got != tc.want {
			t.Errorf("clampScore(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestCacheSource_Fetch_EmptyTaskID verifies CacheSource rejects empty taskID.
// Tests defensive check for invalid input.
func TestCacheSource_Fetch_EmptyTaskID(t *testing.T) {
	cs := CacheSource{
		Traces: map[string]Trace{
			"valid-task::qwen3.7-plus": {TaskID: "valid-task", Model: ModelQwen37Plus},
		},
	}

	_, ok := cs.Fetch("", ModelQwen37Plus)
	if ok {
		t.Fatal("want ok=false for empty taskID, got true")
	}
}

// TestSummarize_EmptyGroup verifies summarize handles empty case slice.
// Tests edge case where model has no cases to aggregate.
func TestSummarize_EmptyGroup(t *testing.T) {
	stats := summarize([]Case{})

	if stats.Count != 0 {
		t.Fatalf("want Count=0, got %d", stats.Count)
	}
	if stats.MeanScore != 0 {
		t.Fatalf("want MeanScore=0, got %v", stats.MeanScore)
	}
	if stats.MedianSteps != 0 {
		t.Fatalf("want MedianSteps=0, got %d", stats.MedianSteps)
	}
	if stats.Completions != 0 {
		t.Fatalf("want Completions=0, got %d", stats.Completions)
	}
}
