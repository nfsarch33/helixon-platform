// registry_test.go — TDD-style tests for Registry.Add / Registry.Run
// and the SyncSource adapter. The four behaviour targets in the brief
// (Registry.Add, Registry.Run, Report.Aggregate, Report.Score) are
// each covered here or in report_test.go.
package helixoneval

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestRegistry_Add_InsertsAndRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Case{ID: "a", Task: "t1", Model: ModelQwen37Plus, Score: 0.8}); err != nil {
		t.Fatalf("first Add: unexpected error: %v", err)
	}
	if err := r.Add(Case{ID: "a", Task: "t1", Model: ModelQwen37Plus, Score: 0.9}); !errors.Is(err, ErrDuplicateCase) {
		t.Fatalf("second Add: want ErrDuplicateCase, got %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len: want 1 got %d", r.Len())
	}
}

func TestRegistry_Add_EmptyIDReturnsError(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Case{}); err == nil {
		t.Fatalf("want error for empty ID, got nil")
	}
}

func TestRegistry_Upsert_OverwritesSameID(t *testing.T) {
	r := NewRegistry()
	if err := r.Upsert(Case{ID: "a", Score: 0.5}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := r.Upsert(Case{ID: "a", Score: 0.9}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	c, ok := r.Get("a")
	if !ok {
		t.Fatalf("Upsert lost the case")
	}
	if c.Score != 0.9 {
		t.Fatalf("Upsert did not overwrite: got %v", c.Score)
	}
}

func TestRegistry_Run_StoresCasePerModel(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)
	ids, err := runner.Run("multi-step coding", AllModels())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ids) != len(AllModels()) {
		t.Fatalf("Run: want %d stored ids got %d", len(AllModels()), len(ids))
	}
	if reg.Len() != len(AllModels()) {
		t.Fatalf("Registry.Len: want %d got %d", len(AllModels()), reg.Len())
	}
	for _, m := range AllModels() {
		c, ok := reg.Get("multi-step coding::" + string(m))
		if !ok {
			t.Fatalf("missing case for model %s", m)
		}
		if c.Score < 0.7 {
			t.Fatalf("case %s scored %.3f, expected ≥0.7", c.ID, c.Score)
		}
	}
}

func TestRegistry_Run_NoModelsReturnsError(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	runner := NewRunner(NewRegistry(), src)
	if _, err := runner.Run("t", nil); err == nil {
		t.Fatalf("want error for empty model list")
	}
}

func TestRegistry_Run_EmptyTaskReturnsError(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	runner := NewRunner(NewRegistry(), src)
	if _, err := runner.Run("", AllModels()); err == nil {
		t.Fatalf("want error for empty task")
	}
}

func TestRegistry_Run_NoTracesReturnsError(t *testing.T) {
	runner := NewRunner(NewRegistry(), NullSource{})
	if _, err := runner.Run("missing", AllModels()); err == nil {
		t.Fatalf("want error when no traces available")
	}
}

func TestRegistry_RunAll_IteratesCatalog(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	reg := NewRegistry()
	runner := NewRunner(reg, src)
	got, err := runner.RunAll(AllModels(), GoldenCatalog())
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	want := len(GoldenTasks()) * len(AllModels())
	if got != want {
		t.Fatalf("RunAll: want %d stored cases got %d", want, got)
	}
}

func TestRegistry_IDs_PreservesInsertionOrder(t *testing.T) {
	r := NewRegistry()
	ids := []string{"c", "a", "b"}
	for _, id := range ids {
		_ = r.Upsert(Case{ID: id})
	}
	got := r.IDs()
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("IDs[%d]: want %s got %s", i, ids[i], got[i])
		}
	}
}

// Stress test mirroring the Sprint 17 svcregistry concurrent register
// regression. Brief specified race-clean; this test asserts the
// invariant under parallel Upsert.
func TestRegistry_ConcurrentUpsert_RaceClean(t *testing.T) {
	r := NewRegistry()
	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = r.Upsert(Case{ID: keyFromInt(i), Score: float64(i) / 1000.0})
		}(i)
	}
	wg.Wait()
	if r.Len() != N {
		t.Fatalf("want %d, got %d", N, r.Len())
	}
}

func TestSynthSource_DeterministicAcrossRuns(t *testing.T) {
	a := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	b := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	for _, task := range GoldenTasks() {
		for _, model := range AllModels() {
			x, _ := a.Fetch(task, model)
			y, _ := b.Fetch(task, model)
			if !approxEqual(x.Score(), y.Score(), 1e-12) {
				t.Fatalf("non-deterministic score for %s on %s: %v vs %v",
					task, model, x.Score(), y.Score())
			}
			for rubric, vx := range x.RubricScores {
				vy, ok := y.RubricScores[rubric]
				if !ok {
					t.Fatalf("missing rubric %s in second run", rubric)
				}
				if !approxEqual(vx, vy, 1e-12) {
					t.Fatalf("non-deterministic per-rubric for %s/%s/%s: %v vs %v",
						task, model, rubric, vx, vy)
				}
			}
		}
	}
}

func TestSynthSource_EveryTaskPassesThreshold(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	for _, task := range GoldenTasks() {
		tr, ok := src.Fetch(task, ModelQwen37Max)
		if !ok {
			t.Fatalf("synth missing trace for %s", task)
		}
		s := mean(tr.RubricScores)
		if s < 0.7 {
			t.Fatalf("task %s below 0.7: %v", task, s)
		}
	}
}

func TestSynthSource_AllCasesHaveValidRubricSet(t *testing.T) {
	src := NewSynthSource(parseTime(t, "2026-07-05T10:00:00Z"))
	tr, _ := src.Fetch(GoldenTasks()[0], ModelQwen37Max)
	for _, id := range RubricIDs {
		v, ok := tr.RubricScores[id]
		if !ok {
			t.Fatalf("missing rubric %s", id)
		}
		if v < 0 || v > 1 {
			t.Fatalf("rubric %s out of [0,1]: %v", id, v)
		}
	}
}

func TestCacheSource_KeyFormatMatchesRunner(t *testing.T) {
	cache := CacheSource{Traces: map[string]Trace{
		"t1::" + string(ModelQwen37Plus): {TaskID: "t1", Model: ModelQwen37Plus, RubricScores: map[string]float64{"correctness": 0.85}},
	}}
	if _, ok := cache.Fetch("t1", ModelQwen37Plus); !ok {
		t.Fatalf("expected trace for t1 on qwen3.7-plus")
	}
	if _, ok := cache.Fetch("t1", ModelMiniMaxM3); ok {
		t.Fatalf("did not expect t1 trace for MiniMax-M3")
	}
}

func TestGoldenCatalog_HasFiveTasks(t *testing.T) {
	if len(GoldenTasks()) != 5 {
		t.Fatalf("golden set must have 5 tasks, got %d", len(GoldenTasks()))
	}
	seen := map[string]bool{}
	for _, task := range GoldenTasks() {
		if seen[task] {
			t.Fatalf("duplicate golden task: %s", task)
		}
		seen[task] = true
	}
}

func TestGoldenTasks_ContainsRequiredTasks(t *testing.T) {
	cases := []string{
		"long-running context retention",
		"self-improvement loop termination",
		"multi-step coding",
		"eval rubric application",
		"PlanSync PR creation",
	}
	got := strings.Join(GoldenTasks(), "|")
	for _, want := range cases {
		if !strings.Contains(got, want) {
			t.Fatalf("golden task list missing %q (got %q)", want, got)
		}
	}
}

func TestStaticCatalog_TasksSorted(t *testing.T) {
	s := StaticCatalog{TasksList: []string{"c", "a", "b"}}
	out := s.Tasks()
	if out[0] != "a" || out[1] != "b" || out[2] != "c" {
		t.Fatalf("want sorted a,b,c got %v", out)
	}
}
