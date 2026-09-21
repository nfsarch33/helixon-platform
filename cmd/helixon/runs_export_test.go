// v18846-2 RED tests: the runs export emits one NDJSON row per iteration,
// every row of a run carries the same run_id and ticket_id, the verdict
// trio round-trips, and a recovered run exports exactly once.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
)

func exportTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.db")
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	s, err := agent.NewSessionStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	t.Setenv("HLXN_EXPORT_TEST_DB", path)
	return path
}

// seedTicketRun creates one ticket run with two assistant iterations and a
// passed verdict. Returns the ticket id used.
func seedTicketRun(t *testing.T, path, runID, ticketID string) {
	t.Helper()
	ctx := context.Background()
	s, err := agent.NewSessionStore(ctx, "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	sess, err := s.CreateSession(ctx, "agent-export-test", map[string]string{"channel": "ticket"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, _, err := s.StartRun(ctx, runID, sess.ID, "fix the flaky build", map[string]string{"channel": "ticket", "ticket_id": ticketID}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if ok, err := s.ClaimRun(ctx, runID, "worker-1", time.Minute); err != nil || !ok {
		t.Fatalf("ClaimRun: ok=%v err=%v", ok, err)
	}
	for i, content := range []string{"planning the fix", "applied the fix"} {
		tools := json.RawMessage(`[]`)
		if i == 1 {
			tools = json.RawMessage(`[{"id":"c1","function":{"name":"file_write"}}]`)
		}
		if _, err := s.AppendTurn(ctx, sess.ID, agent.RoleAssistant, content, tools, "", 5+i, 7+i); err != nil {
			t.Fatalf("AppendTurn %d: %v", i, err)
		}
	}
	if _, err := s.FinishRun(ctx, runID, "worker-1", agent.RunCompleted, &agent.RunResult{
		Mutated: true, VerifierPassed: true, Iterations: 2, TokensIn: 11, TokensOut: 15,
		FinalContent: "done",
	}, nil); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
}

func decodeExport(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode NDJSON: %v", err)
		}
		rows = append(rows, m)
	}
	return rows
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestExport_OneRowPerIteration_SameRunID: two assistant iterations, two
// rows, same run_id and ticket_id, verdict non-null and true, mutated true.
func TestExport_OneRowPerIteration_SameRunID(t *testing.T) {
	path := exportTestDB(t)
	seedTicketRun(t, path, "run-x1", "T-100")

	db := openRaw(t, path)
	rows, err := exportRuns(context.Background(), db, exportFilter{Ticket: "T-100"})
	if err != nil {
		t.Fatalf("exportRuns: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per iteration)", len(rows))
	}
	for i, r := range rows {
		if r["run_id"] != "run-x1" {
			t.Errorf("row %d run_id = %v, want run-x1", i, r["run_id"])
		}
		if r["ticket_id"] != "T-100" {
			t.Errorf("row %d ticket_id = %v, want T-100", i, r["ticket_id"])
		}
		if r["verifier_passed"] != true {
			t.Errorf("row %d verifier_passed = %v, want true", i, r["verifier_passed"])
		}
		if r["mutated"] != true {
			t.Errorf("row %d mutated = %v, want true", i, r["mutated"])
		}
		if r["status"] != "completed" {
			t.Errorf("row %d status = %v, want completed", i, r["status"])
		}
	}
	if rows[0]["iteration"] == rows[1]["iteration"] {
		t.Errorf("iterations must differ per row: %v", rows[0]["iteration"])
	}
}

// TestExport_TicketFilterScopesToThatTicket: a second ticket's rows must
// not leak into a filtered export (Q1a: ticket-runs-only scope).
func TestExport_TicketFilterScopesToThatTicket(t *testing.T) {
	path := exportTestDB(t)
	seedTicketRun(t, path, "run-a", "T-A")
	seedTicketRun(t, path, "run-b", "T-B")

	db := openRaw(t, path)
	rows, err := exportRuns(context.Background(), db, exportFilter{Ticket: "T-A"})
	if err != nil {
		t.Fatalf("exportRuns: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (only T-A's iterations)", len(rows))
	}
	for i, r := range rows {
		if r["run_id"] != "run-a" {
			t.Errorf("row %d leaked run %v into T-A export", i, r["run_id"])
		}
	}
}

// TestExport_RecoveredRun_ExportedOnce: a run the recovery sweep finished
// (same FinishRun path, after attempts > 1) exports its iterations exactly
// once — no duplicate (run_id, iteration) pairs.
func TestExport_RecoveredRun_ExportedOnce(t *testing.T) {
	path := exportTestDB(t)
	ctx := context.Background()

	s, err := agent.NewSessionStore(ctx, "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()
	sess, err := s.CreateSession(ctx, "agent-recovery-test", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, _, err := s.StartRun(ctx, "run-rec", sess.ID, "crash-prone work", map[string]string{"channel": "ticket", "ticket_id": "T-R"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// worker-1's lease is already lapsed: it crashed without renewing.
	if ok, err := s.ClaimRun(ctx, "run-rec", "worker-1", -time.Minute); err != nil || !ok {
		t.Fatalf("ClaimRun (lapsed): %v", err)
	}
	if _, err := s.AppendTurn(ctx, sess.ID, agent.RoleAssistant, "attempt one", json.RawMessage(`[]`), "", 1, 1); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	// Recovery: a second claim after the first worker died, then finish.
	if ok, err := s.ClaimRun(ctx, "run-rec", "worker-2", time.Minute); err != nil || !ok {
		t.Fatalf("re-ClaimRun: %v", err)
	}
	if _, err := s.AppendTurn(ctx, sess.ID, agent.RoleAssistant, "attempt two", json.RawMessage(`[]`), "", 1, 1); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if _, err := s.FinishRun(ctx, "run-rec", "worker-2", agent.RunCompleted, &agent.RunResult{
		Mutated: true, VerifierPassed: true, Iterations: 2,
	}, nil); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	db := openRaw(t, path)
	rows, err := exportRuns(ctx, db, exportFilter{Ticket: "T-R"})
	if err != nil {
		t.Fatalf("exportRuns: %v", err)
	}
	seen := map[string]bool{}
	for i, r := range rows {
		key := r["run_id"].(string) + "/" + fmt.Sprint(r["iteration"])
		if seen[key] {
			t.Fatalf("row %d duplicates %s — recovered run exported twice", i, key)
		}
		seen[key] = true
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (two iterations, once)", len(rows))
	}
}

// TestExport_NoVerifierRunNullsNotFalse: a non-mutated completed run
// exports verifier_passed=null (must-be-absent control).
func TestExport_NoVerifierRunNullsNotFalse(t *testing.T) {
	path := exportTestDB(t)
	ctx := context.Background()
	s, err := agent.NewSessionStore(ctx, "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()
	sess, _ := s.CreateSession(ctx, "agent-null-test", nil)
	if _, _, err := s.StartRun(ctx, "run-null", sess.ID, "read-only question", map[string]string{"channel": "ticket", "ticket_id": "T-N"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if ok, err := s.ClaimRun(ctx, "run-null", "w", time.Minute); err != nil || !ok {
		t.Fatalf("ClaimRun: %v", err)
	}
	if _, err := s.AppendTurn(ctx, sess.ID, agent.RoleAssistant, "answer only", json.RawMessage(`[]`), "", 1, 1); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if _, err := s.FinishRun(ctx, "run-null", "w", agent.RunCompleted, &agent.RunResult{FinalContent: "answer"}, nil); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	db := openRaw(t, path)
	rows, err := exportRuns(ctx, db, exportFilter{Ticket: "T-N"})
	if err != nil {
		t.Fatalf("exportRuns: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	vp, ok := rows[0]["verifier_passed"]
	if !ok || vp != nil {
		t.Fatalf("verifier_passed = %v (present=%v), want JSON null", vp, ok)
	}
}
