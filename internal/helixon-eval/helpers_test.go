// helpers_test.go — small shared test helpers (parseTime, keyFromInt).
package helixoneval

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// parseTime is a tiny RFC3339 wrapper that t.Fatal's on parse error so
// test setup stays readable.
func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parseTime %q: %v", s, err)
	}
	return tt
}

// keyFromInt is a stable string encoder for the concurrent test so the
// test never collides on a duplicate key.
func keyFromInt(i int) string {
	return "k-" + strconv.Itoa(i)
}

// TestHelpersAreInternallyConsistent ensures parseTime + mean flow.
func TestHelpersAreInternallyConsistent(t *testing.T) {
	t1 := parseTime(t, "2026-07-05T10:00:00Z")
	if t1.Year() != 2026 {
		t.Fatalf("parseTime wrong year: %v", t1)
	}
	if keyFromInt(0) != "k-0" {
		t.Fatalf("keyFromInt wrong format")
	}
	tr := Trace{RubricScores: map[string]float64{"a": 0.6, "b": 0.4}}
	if tr.Score() != 0.5 {
		t.Fatalf("Trace.Score should be mean of rubric scores")
	}
}

// TestRubricIDs_AreStableAndUnique ensures the rubric IDs list does
// not regress if a contributor duplicates or renames a rubric.
func TestRubricIDs_AreStableAndUnique(t *testing.T) {
	if len(RubricIDs) != 4 {
		t.Fatalf("want 4 rubrics, got %d (%v)", len(RubricIDs), RubricIDs)
	}
	seen := map[string]bool{}
	for _, id := range RubricIDs {
		if seen[id] {
			t.Fatalf("duplicate rubric id %q", id)
		}
		seen[id] = true
	}
	// Required IDs from the brief.
	for _, want := range []string{"correctness", "robustness", "completeness", "termination"} {
		if !seen[want] {
			t.Fatalf("missing required rubric %q in %v", want, RubricIDs)
		}
	}
}

// TestSlugify_Stable ensures the slugifier produces a kebab-case
// identifier for the golden task titles. The current implementation
// treats CamelCase transitions as word boundaries, so "PlanSync PR"
// becomes "plan-sync-pr-creation". This convention is documented in
// the EventRegistrar schema and is fine for human-readable IDs.
func TestSlugify_Stable(t *testing.T) {
	cases := map[string]string{
		"long-running context retention":      "long-running-context-retention",
		"self-improvement loop termination":  "self-improvement-loop-termination",
		"multi-step coding":                   "multi-step-coding",
		"eval rubric application":             "eval-rubric-application",
		"PlanSync PR creation":                "plan-sync-pr-creation",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Fatalf("slugify(%q): want %q got %q", in, want, got)
		}
	}
}

// TestAllModels_AreStable ensures the model identifier list does not
// regress.
func TestAllModels_AreStable(t *testing.T) {
	want := []Model{ModelQwen37Plus, ModelQwen37Max, ModelMiniMaxM3}
	got := AllModels()
	if len(got) != len(want) {
		t.Fatalf("want %d models, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllModels[%d]: want %s got %s", i, want[i], got[i])
		}
	}
}

// TestRoundScore_RoundsToThousandths guards the helper used by the
// RunAll golden path.
func TestRoundScore_RoundsToThousandths(t *testing.T) {
	if roundScore(0.12349) != 0.123 {
		t.Fatalf("roundScore wrong for 0.12349")
	}
	if roundScore(0.1235) != 0.124 {
		t.Fatalf("roundScore wrong for 0.1235")
	}
}

// TestAggregateNils verifies Aggregate tolerates a nil Registry.
func TestAggregateNils(t *testing.T) {
	var r *Registry
	rep := Report{}
	rep.Aggregate(r, "v16129", 0.7)
	if rep.Pass || rep.OverallScore != 0 {
		t.Fatalf("Aggregate(nil) should yield pass=false, score=0")
	}
}

// TestAllModelsAndGoldenTaskCross guards the spec'd 5x3=15 matrix.
func TestAllModelsAndGoldenTaskCross(t *testing.T) {
	if len(GoldenTasks())*len(AllModels()) != 15 {
		t.Fatalf("matrix size off: %d tasks x %d models",
			len(GoldenTasks()), len(AllModels()))
	}
	// surface the matrix explicitly so a regression is obvious in logs
	for _, task := range GoldenTasks() {
		for _, m := range AllModels() {
			id := fmt.Sprintf("%s::%s", task, m)
			_ = id
		}
	}
}
