// v18846-2 RED tests: FinishRun must persist the verifier verdict trio on
// the run row, NULL when no verifier was applicable. The export chain's
// must-be-present control (verdict non-null on every completed mutated run)
// and must-be-absent control (null, never false, without a verifier run)
// both hang off these columns.
package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func verdictTestStore(t *testing.T) *SessionStore {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "runs.db") + "?_pragma=busy_timeout(5000)"
	s, err := NewSessionStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func startClaimedRun(t *testing.T, s *SessionStore, id string) string {
	t.Helper()
	ctx := context.Background()
	sess, err := s.CreateSession(ctx, "agent-verdict-test", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, created, err := s.StartRun(ctx, id, sess.ID, "do the thing", map[string]string{"channel": "ticket", "ticket_id": "T-1"}); err != nil || !created {
		t.Fatalf("StartRun: created=%v err=%v", created, err)
	}
	if ok, err := s.ClaimRun(ctx, id, "worker-1", time.Minute); err != nil || !ok {
		t.Fatalf("ClaimRun: ok=%v err=%v", ok, err)
	}
	return sess.ID
}

// verdictRow reads the three columns straight off the table.
func verdictRow(t *testing.T, s *SessionStore, id string) (verifierPassed, mutated, verifierFailures any) {
	t.Helper()
	row := s.db.QueryRow(`SELECT verifier_passed, mutated, verifier_failures FROM runs WHERE id = ?`, id)
	if err := row.Scan(&verifierPassed, &mutated, &verifierFailures); err != nil {
		t.Fatalf("select verdict row: %v", err)
	}
	return
}

// TestFinishRun_PersistsVerifierVerdict is the must-be-present control.
func TestFinishRun_PersistsVerifierVerdict(t *testing.T) {
	s := verdictTestStore(t)
	ctx := context.Background()
	startClaimedRun(t, s, "run-v1")
	_, err := s.FinishRun(ctx, "run-v1", "worker-1", RunCompleted, &RunResult{
		Mutated: true, VerifierPassed: true, VerifierFailures: 2,
		Iterations: 3, TokensIn: 10, TokensOut: 20, FinalContent: "done",
	}, nil)
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	vp, mut, vf := verdictRow(t, s, "run-v1")
	if vp != int64(1) {
		t.Fatalf("verifier_passed = %v, want 1", vp)
	}
	if mut != int64(1) {
		t.Fatalf("mutated = %v, want 1", mut)
	}
	if vf != int64(2) {
		t.Fatalf("verifier_failures = %v, want 2", vf)
	}
}

// TestFinishRun_NoVerifierRun_LeavesNull is the must-be-absent control: a
// run that never mutated has no verdict, and the export must show NULL —
// never false, which would read as "checked and failed".
func TestFinishRun_NoVerifierRun_LeavesNull(t *testing.T) {
	s := verdictTestStore(t)
	ctx := context.Background()
	startClaimedRun(t, s, "run-v2")
	_, err := s.FinishRun(ctx, "run-v2", "worker-1", RunCompleted, &RunResult{
		Iterations: 1, TokensIn: 1, TokensOut: 1, FinalContent: "read-only answer",
	}, nil)
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	vp, mut, vf := verdictRow(t, s, "run-v2")
	if vp != nil {
		t.Fatalf("verifier_passed = %v, want NULL", vp)
	}
	if vf != nil {
		t.Fatalf("verifier_failures = %v, want NULL", vf)
	}
	if mut != nil && mut != int64(0) {
		t.Fatalf("mutated = %v, want 0 or NULL", mut)
	}
}

// TestFinishRun_FailedVerdictPersisted: a mutated run whose verifier did
// not pass records false — the honest "checked and failed" signal.
func TestFinishRun_FailedVerdictPersisted(t *testing.T) {
	s := verdictTestStore(t)
	ctx := context.Background()
	startClaimedRun(t, s, "run-v3")
	_, err := s.FinishRun(ctx, "run-v3", "worker-1", RunNeedsHuman, &RunResult{
		Mutated: true, VerifierPassed: false, VerifierFailures: 3,
	}, context.DeadlineExceeded)
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	vp, mut, vf := verdictRow(t, s, "run-v3")
	if vp != int64(0) {
		t.Fatalf("verifier_passed = %v, want 0 (checked and failed)", vp)
	}
	if mut != int64(1) {
		t.Fatalf("mutated = %v, want 1", mut)
	}
	if vf != int64(3) {
		t.Fatalf("verifier_failures = %v, want 3", vf)
	}
}

// TestMigration_IdempotentOnExistingDB: a database created BEFORE the
// verdict columns existed gains them on open, keeps its rows (NULL
// verdicts — honestly unknown), and a second open is a no-op.
func TestMigration_IdempotentOnExistingDB(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "legacy.db") + "?_pragma=busy_timeout(5000)"

	// First open creates the current schema; drop the new columns to
	// simulate a legacy database, with one surviving row.
	s, err := NewSessionStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sess, err := s.CreateSession(context.Background(), "legacy", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, _, err := s.StartRun(context.Background(), "legacy-run", sess.ID, "old work", nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for _, col := range []string{"verifier_passed", "mutated", "verifier_failures"} {
		if _, err := s.db.Exec(`ALTER TABLE runs DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s (simulate legacy): %v", col, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: migration must re-add the columns idempotently.
	s2, err := NewSessionStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("reopen with legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	var vp, vf any
	row := s2.db.QueryRow(`SELECT verifier_passed, verifier_failures FROM runs WHERE id = ?`, "legacy-run")
	if err := row.Scan(&vp, &vf); err != nil {
		t.Fatalf("legacy row unreadable after migration: %v", err)
	}
	if vp != nil || vf != nil {
		t.Fatalf("legacy row verdicts = %v/%v, want NULL/NULL (unknown, not false)", vp, vf)
	}
}
