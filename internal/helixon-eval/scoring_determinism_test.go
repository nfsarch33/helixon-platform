// scoring_determinism_test.go — the scoring path must be a pure
// function of its inputs.
//
// Go randomises map iteration order and float64 addition is not
// associative, so any score accumulated by ranging over a map varies
// by one ULP between runs on identical input. That made
// TestRun_ConflictResolution_LastWriteWins flaky (~30% on main): the
// same trace scored twice in one process produced 0.914 on one call
// and 0.9139999999999999 on the next.
//
// These tests pin the contract at the arithmetic itself rather than
// at any one caller's assertion.
package helixoneval

import (
	"sort"
	"testing"
)

// orderSensitiveRubrics is the exact rubric set SynthSource produces
// for "multi-step coding"::qwen3.7-plus — a base of 0.904 on three
// rubrics and 0.944 on correctness. Summed left-to-right these give
// 3.6560000000000001; summed with 0.944 first they give
// 3.6559999999999997. It is the real failing input, not a contrived
// one.
func orderSensitiveRubrics() map[string]float64 {
	return map[string]float64{
		"correctness":  0.944,
		"robustness":   0.904,
		"completeness": 0.904,
		"termination":  0.904,
	}
}

// TestMean_IsOrderIndependent calls mean on one map many times. Map
// iteration order is re-randomised per range statement, so an
// order-dependent implementation returns both ULPs across this many
// calls with overwhelming probability.
func TestMean_IsOrderIndependent(t *testing.T) {
	m := orderSensitiveRubrics()
	want := mean(m)
	for i := 0; i < 2000; i++ {
		if got := mean(m); got != want {
			t.Fatalf("mean is order-dependent: call %d got %.17g, want %.17g", i, got, want)
		}
	}
}

// TestMean_MatchesSortedKeyOrder states the arithmetic mean is
// specifically defined as the sorted-key sum, so a future refactor
// that changes the summation order is caught even if it happens to be
// internally consistent.
func TestMean_MatchesSortedKeyOrder(t *testing.T) {
	m := orderSensitiveRubrics()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sum float64
	for _, k := range keys {
		sum += m[k]
	}
	want := sum / float64(len(m))
	if got := mean(m); got != want {
		t.Fatalf("mean should sum in sorted-key order: got %.17g, want %.17g", got, want)
	}
}

// TestTraceScore_IsStableAcrossRepeatedCalls covers the exported
// surface: Trace.Score is what external readers call, and it must
// agree with itself.
func TestTraceScore_IsStableAcrossRepeatedCalls(t *testing.T) {
	tr := Trace{RubricScores: orderSensitiveRubrics()}
	want := tr.Score()
	for i := 0; i < 2000; i++ {
		if got := tr.Score(); got != want {
			t.Fatalf("Trace.Score is order-dependent: call %d got %.17g, want %.17g", i, got, want)
		}
	}
}

// TestRunner_RescoringSameTraceIsBitIdentical is the end-to-end shape
// of the original flake: run the same task/model twice through the
// Runner and require the stored Score to be bit-identical, not merely
// close. This is the assertion
// TestRun_ConflictResolution_LastWriteWins makes once; here it is
// repeated enough to fail deterministically if the scorer regresses.
func TestRunner_RescoringSameTraceIsBitIdentical(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-14T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	if _, err := runner.Run("multi-step coding", []Model{ModelQwen37Plus}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	first, ok := reg.Get("multi-step coding::qwen3.7-plus")
	if !ok {
		t.Fatal("first run: case missing from registry")
	}

	for i := 0; i < 500; i++ {
		if _, err := runner.Run("multi-step coding", []Model{ModelQwen37Plus}); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
		again, ok := reg.Get("multi-step coding::qwen3.7-plus")
		if !ok {
			t.Fatalf("re-run %d: case missing from registry", i)
		}
		if again.Score != first.Score {
			t.Fatalf("re-run %d: score not reproducible: got %.17g, want %.17g",
				i, again.Score, first.Score)
		}
	}
}

// TestAggregate_OverallScoreIsOrderIndependent covers the second
// order-dependent accumulation: Report.Aggregate summed the per-model
// means by ranging over the ModelStats map. That one decides Pass, so
// a run sitting exactly on the threshold could flip verdict between
// identical runs.
func TestAggregate_OverallScoreIsOrderIndependent(t *testing.T) {
	// Four models whose per-model means are the same order-sensitive
	// addends: 0.944 + 0.904 + 0.904 + 0.904.
	scores := map[Model]float64{
		ModelQwen37Plus: 0.944,
		ModelQwen37Max:  0.904,
		ModelMiniMaxM3:  0.904,
		ModelOfflineFix: 0.904,
	}
	reg := NewRegistry()
	for _, m := range []Model{ModelQwen37Plus, ModelQwen37Max, ModelMiniMaxM3, ModelOfflineFix} {
		c := Case{
			ID:                "order-sensitive::" + string(m),
			Task:              "order-sensitive",
			Model:             m,
			Score:             scores[m],
			Steps:             5,
			TerminationReason: "completed",
			RubricScores:      orderSensitiveRubrics(),
		}
		if err := reg.Add(c); err != nil {
			t.Fatalf("Add %s: %v", m, err)
		}
	}

	var rep Report
	rep.Aggregate(reg, "v18517", 0.7)
	want := rep.OverallScore

	for i := 0; i < 2000; i++ {
		var again Report
		again.Aggregate(reg, "v18517", 0.7)
		if again.OverallScore != want {
			t.Fatalf("OverallScore is order-dependent: call %d got %.17g, want %.17g",
				i, again.OverallScore, want)
		}
	}
}
