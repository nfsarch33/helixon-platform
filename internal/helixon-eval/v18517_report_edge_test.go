// v18517_report_edge_test.go — TDD tests for the v18517 report-edge
// integration. Verifies SetEdgeResults attaches an edge-test
// summary that the subsequent WriteText call renders as a new
// "Edge-test coverage" section. Without a SetEdgeResults call, the
// report renders as it did before v18517 (no edge section).
package helixoneval

import (
	"strings"
	"testing"
)

// TestReport_SetEdgeResults_RendersSection verifies that attaching
// edge results produces the new "Edge-test coverage" section under
// the per-model block and before the per-case block.
func TestReport_SetEdgeResults_RendersSection(t *testing.T) {
	reg := NewRegistry()
	mustAdd(t, reg, Case{
		ID: "multi-step coding::qwen3.7-plus",
		Task: "multi-step coding", Model: ModelQwen37Plus,
		Score: 0.85,
		RubricScores: map[string]float64{
			"correctness": 0.85, "robustness": 0.85, "completeness": 0.85, "termination": 0.85,
		},
		Steps: 8, TerminationReason: "completed",
	})

	rep := Report{}
	rep.Aggregate(reg, "v18517", 0.7)

	// Attach edge results.
	rep.SetEdgeResults(EdgeResults{
		Total:  8,
		Passed: 8,
		Failed: 0,
		Entries: []EdgeTestEntry{
			{Name: "TestRun_ConflictResolution_LastWriteWins", Passed: true, Source: "v18517_edge_test.go"},
			{Name: "TestRun_EmptyTaskID_Rejected", Passed: true, Source: "v18517_edge_test.go"},
			{Name: "TestRun_Concurrent_NoDuplicateIDs", Passed: true, Source: "v18517_edge_test.go"},
		},
	})

	var buf strings.Builder
	if err := rep.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "## Edge-test coverage") {
		t.Fatal("want ## Edge-test coverage section in output, missing")
	}
	if !strings.Contains(got, "Total: 8") {
		t.Fatal("want Total: 8 in output, missing")
	}
	if !strings.Contains(got, "TestRun_ConflictResolution_LastWriteWins | PASS") {
		t.Fatal("want edge-test row in output, missing")
	}
	// Section ordering: edge-test block precedes per-case block.
	edgeIdx := strings.Index(got, "## Edge-test coverage")
	perCaseIdx := strings.Index(got, "## Per-case")
	if edgeIdx < 0 || perCaseIdx < 0 || edgeIdx > perCaseIdx {
		t.Fatalf("want Edge-test section before Per-case; got edge=%d perCase=%d", edgeIdx, perCaseIdx)
	}
}

// TestReport_NoEdgeResults_OmitsSection verifies that callers who
// never call SetEdgeResults see no "Edge-test coverage" section —
// preserves the v16129-era contract for callers that haven't
// adopted the v18517 wrap.
func TestReport_NoEdgeResults_OmitsSection(t *testing.T) {
	reg := NewRegistry()
	rep := Report{}
	rep.Aggregate(reg, "v18104", 0.7)

	var buf strings.Builder
	if err := rep.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(buf.String(), "## Edge-test coverage") {
		t.Fatal("want NO edge-test section when SetEdgeResults not called")
	}
}

// TestReport_SetEdgeResults_NilClears verifies that calling
// SetEdgeResults with EdgeResults{} clears any prior attachment.
// Idempotency check that supports the CLI's "no edge suite" path.
func TestReport_SetEdgeResults_NilClears(t *testing.T) {
	rep := Report{}
	rep.SetEdgeResults(EdgeResults{Total: 1, Passed: 1, Failed: 0})
	rep.SetEdgeResults(EdgeResults{}) // clear

	if _, ok := edgeResultsByReport[&rep]; ok {
		t.Fatal("want edge results cleared after empty SetEdgeResults")
	}
}
