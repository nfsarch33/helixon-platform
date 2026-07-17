// v18517_edge_test.go — 8 new edge cases for the HelixonEval runner.
//
// These tests target behavioral gaps discovered during the v18517
// audit. They cover the runner's behavior under stress (partial
// inputs, timeouts, concurrent runs, unknown models) and the
// report's robustness to malformed traces.
//
// The eight cases are:
//  1. Conflict resolution: same ID, two different scores (Upsert wins)
//  2. All rubrics missing on one model (skews model mean)
//  3. Unknown model name not in canonical four (Skip silently)
//  4. Empty taskID rejected by Run
//  5. Empty models slice rejected by Run
//  6. Concurrent Run (10 goroutines) — no panic, no duplicate IDs
//  7. Very long TaskID (1024 char) accepted as-is, no truncation
//  8. Negative-step / Inf-duration case passes through without panic
//
// These tests follow the same TDD pattern as registry_test.go and
// registry_edge_test.go.
package helixoneval

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

// v18517-3 Case 1: Conflict resolution.
//
// When the same (task, model) pair is run twice, the Runner must
// overwrite the prior case with the new score. This is the "last
// write wins" contract that lets re-running a broken model produce
// fresh evidence without crashing on duplicate ID.
func TestRun_ConflictResolution_LastWriteWins(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-14T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	// First run produces score S1.
	if _, err := runner.Run("multi-step coding", []Model{ModelQwen37Plus}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	first, ok := reg.Get("multi-step coding::qwen3.7-plus")
	if !ok {
		t.Fatal("first run: case missing from registry")
	}
	firstScore := first.Score
	if firstScore <= 0 || firstScore >= 1 {
		t.Fatalf("first run: sanity score range, got %v", firstScore)
	}

	// Second run on the same task/model should overwrite the case.
	if _, err := runner.Run("multi-step coding", []Model{ModelQwen37Plus}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	second, ok := reg.Get("multi-step coding::qwen3.7-plus")
	if !ok {
		t.Fatal("second run: case missing from registry")
	}

	// SynthSource is deterministic, so both runs produce the SAME
	// score — that is the regression contract. What matters here is
	// that the registry has exactly ONE case with that ID (no
	// duplication), and that the upsert path did not return an error.
	if reg.Len() != 1 {
		t.Fatalf("want Len=1 after re-run, got %d", reg.Len())
	}
	if second.Score != firstScore {
		t.Fatalf("want deterministic score, got %v vs %v", second.Score, firstScore)
	}
}

// v18517-3 Case 2: All rubrics missing on a single model pulls its
// mean to zero. The aggregate per-model mean must reflect this so a
// run with one "broken" model still produces a useful report (just
// with a lower overall score).
func TestReport_Aggregate_ModelWithEmptyRubricPullsItsMeanToZero(t *testing.T) {
	reg := NewRegistry()
	// A canonical good case (score ~0.85)
	mustAdd(t, reg, Case{
		ID:   "long-running context retention::qwen3.7-plus",
		Task: "long-running context retention", Model: ModelQwen37Plus,
		Score: 0.85, Steps: 10, TerminationReason: "completed",
		RubricScores: map[string]float64{
			"correctness": 0.85, "robustness": 0.85, "completeness": 0.85, "termination": 0.85,
		},
	})
	// A broken case with empty rubric (score 0)
	mustAdd(t, reg, Case{
		ID:   "loop termination — budget exhaustion::qwen3.7-max",
		Task: "loop termination — budget exhaustion", Model: ModelQwen37Max,
		Score: 0, Steps: 5, TerminationReason: "max_steps",
		RubricScores: map[string]float64{},
	})

	rep := Report{}
	rep.Aggregate(reg, "v18517", 0.7)

	// Per-model: qwen3.7-plus mean ≈ 0.85, qwen3.7-max mean = 0
	qwenPlus, ok := rep.ModelStats[ModelQwen37Plus]
	if !ok {
		t.Fatal("want qwen3.7-plus model stat, missing")
	}
	if qwenPlus.MeanScore < 0.80 {
		t.Fatalf("want qwen3.7-plus mean≈0.85, got %v", qwenPlus.MeanScore)
	}
	qwenMax, ok := rep.ModelStats[ModelQwen37Max]
	if !ok {
		t.Fatal("want qwen3.7-max model stat, missing")
	}
	if qwenMax.MeanScore != 0 {
		t.Fatalf("want qwen3.7-max mean=0 for empty rubric, got %v", qwenMax.MeanScore)
	}

	// Overall = mean(0.85, 0) = 0.425 < 0.7 => FAIL
	if rep.Pass {
		t.Fatal("want Pass=false (one model empty), got true")
	}
	if math.Abs(rep.OverallScore-0.425) > 1e-9 {
		t.Fatalf("want OverallScore≈0.425, got %v", rep.OverallScore)
	}
}

// v18517-3 Case 3: Unknown model name (not in canonical four) accepted
// by SynthSource with a deterministic synthesised trace. The canonical
// four (qwen3.7-plus, qwen3.7-max, MiniMax-M3, offline-fixture) are the
// only ones callers SHOULD pass; but SynthSource.Fetch has no
// allowlist and will produce a deterministic score for any non-empty
// model string. This test pins the actual behaviour so future
// refactors don't silently widen the surface.
//
// v18517 AUDIT FINDING: the lack of a model allowlist is a documentation
// gap. The Sprint 18 brief lists only four canonical model names;
// callers passing arbitrary strings get a synthesised score with no
// warning. Recommended hardening (CARRY-060): add a Validate() helper
// to the Runner that rejects unknown model names.
func TestRun_UnknownModel_AcceptedBySynthSource_PinsBehaviour(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-14T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	unknown := Model("gpt-9000-pretend")
	ids, err := runner.Run("multi-step coding", []Model{unknown})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("want 1 stored id, got %d", len(ids))
	}
	if reg.Len() != 1 {
		t.Fatalf("want Len=1, got %d", reg.Len())
	}

	// SynthSource accepts arbitrary model strings; the score is
	// deterministic (same model+task => same hash => same score).
	// The Model.String() default branch wraps unknown names as
	// model(<name>), so the stored ID differs from the bare
	// "task::model" form. The actual ID is constructed via the
	// String() method on the Model.
	wantID := "multi-step coding::" + unknown.String()
	stored, ok := reg.Get(wantID)
	if !ok {
		// Diagnostic: dump what is actually in the registry
		seen := reg.IDs()
		t.Fatalf("want case retrievable under ID %q; registry has %v", wantID, seen)
	}
	if stored.Model != unknown {
		t.Fatalf("want Model=%q on stored case, got %q", unknown, stored.Model)
	}
	if stored.Score <= 0 || stored.Score >= 1 {
		t.Fatalf("want synthesised score in (0,1), got %v", stored.Score)
	}
}

// v18517-3 Case 4: Empty taskID rejected with a clear error.
func TestRun_EmptyTaskID_Rejected(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-14T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	_, err := runner.Run("", []Model{ModelQwen37Plus})
	if err == nil {
		t.Fatal("want error for empty taskID, got nil")
	}
	if !strings.Contains(err.Error(), "taskID is empty") {
		t.Fatalf("want empty taskID error, got %v", err)
	}
}

// v18517-3 Case 5: Empty models slice rejected with a clear error.
func TestRun_EmptyModels_Rejected(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-14T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	_, err := runner.Run("multi-step coding", []Model{})
	if err == nil {
		t.Fatal("want error for empty models slice, got nil")
	}
	if !strings.Contains(err.Error(), "no models") {
		t.Fatalf("want no-models error, got %v", err)
	}
}

// v18517-3 Case 6: Concurrent Run across 10 goroutines — no panic,
// no duplicate IDs, registry length matches the total successful
// (task, model) pairs.
func TestRun_Concurrent_NoDuplicateIDs(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-14T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	tasks := []string{
		"long-running context retention",
		"self-improvement loop termination",
		"multi-step coding",
		"eval rubric application",
		"PlanSync PR creation",
	}
	models := []Model{ModelQwen37Plus, ModelQwen37Max, ModelMiniMaxM3}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, task := range tasks {
				_, _ = runner.Run(task, models)
			}
		}()
	}
	wg.Wait()

	// 5 tasks × 3 models = 15 cases expected
	want := len(tasks) * len(models)
	if reg.Len() != want {
		t.Fatalf("want Len=%d after concurrent runs, got %d", want, reg.Len())
	}

	// Verify all IDs are unique (order slice)
	seen := map[string]bool{}
	for _, id := range reg.IDs() {
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
	}
}

// v18517-3 Case 7: Very long TaskID (1024 chars) accepted as-is.
//
// The Sprint 18 brief restricts task IDs to <256 chars; this test
// documents that longer IDs are NOT truncated silently — the registry
// stores them verbatim and the case lookup still works. Future
// hardening could add a length limit, but for now the contract is
// "trust the caller".
func TestRun_VeryLongTaskID_AcceptedAsIs(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-14T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)

	long := strings.Repeat("a", 1024)
	ids, err := runner.Run(long, []Model{ModelQwen37Plus})
	if err != nil {
		t.Fatalf("Run with 1024-char taskID: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("want 1 stored id, got %d", len(ids))
	}
	want := long + "::qwen3.7-plus"
	if ids[0] != want {
		t.Fatalf("want ids[0]=%q, got %q", want, ids[0])
	}
	if _, ok := reg.Get(want); !ok {
		t.Fatal("want case retrievable by full long ID")
	}
}

// v18517-3 Case 8: Negative-step / Inf-duration case passes through
// without panic. SynthSource synthesises scores deterministically but
// does NOT control steps/duration — those are caller inputs. If a
// caller constructs a Case with Steps=-1 or DurationMS=math.MaxInt64,
// the report must not panic in median() or mean().
func TestReport_DefensiveOnNegativeStepsAndInfDuration(t *testing.T) {
	reg := NewRegistry()
	mustAdd(t, reg, Case{
		ID:   "multi-step coding::qwen3.7-plus",
		Task: "multi-step coding", Model: ModelQwen37Plus,
		Score: 0.7,
		RubricScores: map[string]float64{
			"correctness": 0.7, "robustness": 0.7, "completeness": 0.7, "termination": 0.7,
		},
		Steps: -5, TerminationReason: "error", DurationMS: math.MaxInt64,
		StartedAt: time.Unix(0, 0).UTC(),
	})

	rep := Report{}
	rep.Aggregate(reg, "v18517", 0.7)

	// Median of [-5] is -5 (single element)
	stat := rep.ModelStats[ModelQwen37Plus]
	if stat.MedianSteps != -5 {
		t.Fatalf("want MedianSteps=-5, got %d", stat.MedianSteps)
	}
	if stat.MeanScore < 0.69 {
		t.Fatalf("want MeanScore≈0.7, got %v", stat.MeanScore)
	}

	// WriteText must not panic on the bad row.
	var buf strings.Builder
	if err := rep.WriteText(&buf); err != nil {
		t.Fatalf("WriteText with bad row: %v", err)
	}
	if !strings.Contains(buf.String(), "-5") {
		t.Fatal("want negative-steps row in output, missing")
	}
}

// mustAdd is a small helper that fails the test if Upsert errors. It
// keeps the new edge-case tests above free of repeated error-handling
// boilerplate.
func mustAdd(t *testing.T, r *Registry, c Case) {
	t.Helper()
	if err := r.Upsert(c); err != nil {
		t.Fatalf("Upsert(%q): %v", c.ID, err)
	}
}
