// Tests for the send-end-email CLI. v17714-1: captures the dry-run
// behaviour before refactoring main() from CC=18 down to ≤6.
package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runSendEndEmail(t *testing.T, args []string, env map[string]string) (int, string, string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}

	origStdout, origStderr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr
	defer func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	}()

	rc := runSendEndEmailCmd(args)

	_ = wOut.Close()
	_ = wErr.Close()
	stdoutBytes, _ := io.ReadAll(rOut)
	stderrBytes, _ := io.ReadAll(rErr)
	return rc, string(stdoutBytes), string(stderrBytes)
}

func extractEndAudit(t *testing.T, stdout string) map[string]any {
	t.Helper()
	start := strings.Index(stdout, "{")
	end := strings.LastIndex(stdout, "}")
	if start < 0 || end <= start {
		t.Fatalf("no JSON object in stdout: %q", stdout)
	}
	var audit map[string]any
	if err := json.Unmarshal([]byte(stdout[start:end+1]), &audit); err != nil {
		t.Fatalf("unmarshal: %v; raw=%q", err, stdout)
	}
	return audit
}

func writeBodyFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "body.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil { //nolint:gosec // G306 test fixture
		t.Fatalf("write body file: %v", err)
	}
	return p
}

func TestSendEndEmail_MissingIdempotencyKey(t *testing.T) {
	rc, _, stderr := runSendEndEmail(t, []string{"--dry-run"}, map[string]string{})
	if rc != 2 {
		t.Errorf("rc = %d; want 2", rc)
	}
	if !strings.Contains(stderr, "idempotency-key") {
		t.Errorf("stderr missing idempotency-key: %q", stderr)
	}
}

func TestSendEndEmail_DryRun(t *testing.T) {
	bodyFile := writeBodyFile(t, "# END\nplan v17713-v17800 closed GREEN\n")
	rc, stdout, stderr := runSendEndEmail(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-end-test-1",
		"--plan", "v17713-v17800",
		"--subject", "[END]",
		"--body-file", bodyFile,
		"--job-id", "v17714-end-1",
	}, map[string]string{})

	if rc != 0 {
		t.Errorf("rc = %d; want 0 (dry-run)", rc)
	}
	if !strings.Contains(stderr, "DRY-RUN") {
		t.Errorf("stderr missing DRY-RUN marker: %q", stderr)
	}
	audit := extractEndAudit(t, stdout)
	if audit["event"] != "send_end_email_attempt" {
		t.Errorf("event = %v; want send_end_email_attempt", audit["event"])
	}
	if audit["idempotency_key"] != "v17714-end-test-1" {
		t.Errorf("idempotency_key = %v", audit["idempotency_key"])
	}
	if audit["result"] != "dry-run" {
		t.Errorf("result = %v; want dry-run", audit["result"])
	}
	if audit["plan"] != "v17713-v17800" {
		t.Errorf("plan = %v", audit["plan"])
	}
	if audit["idempotency_first_call"] != true {
		t.Errorf("idempotency_first_call = %v; want true", audit["idempotency_first_call"])
	}
	if _, ok := audit["blocker"]; !ok {
		t.Errorf("dry-run should include blocker field")
	}
	if _, ok := audit["email_render"]; !ok {
		t.Errorf("dry-run should include email_render field")
	}
}

func TestSendEndEmail_NoCCFlag(t *testing.T) {
	bodyFile := writeBodyFile(t, "# END\nbody\n")
	rc, stdout, _ := runSendEndEmail(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-end-nocc",
		"--plan", "v17713-v17800",
		"--subject", "[END]",
		"--body-file", bodyFile,
		"--no-cc",
	}, map[string]string{})
	if rc != 0 {
		t.Errorf("rc = %d; want 0", rc)
	}
	audit := extractEndAudit(t, stdout)
	// cc field on the audit event should be nil/empty when --no-cc is set
	// (the cc field on the dispatch layer is stripped). We just check the
	// flag was processed without error here.
	if audit["idempotency_key"] != "v17714-end-nocc" {
		t.Errorf("idempotency_key = %v", audit["idempotency_key"])
	}
}

func TestSendEndEmail_AuditDBPath(t *testing.T) {
	bodyFile := writeBodyFile(t, "# END\nbody\n")
	dbPath := filepath.Join(t.TempDir(), "notifydb.sqlite3")
	rc, stdout, _ := runSendEndEmail(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-end-db",
		"--plan", "v17713-v17800",
		"--subject", "[END]",
		"--body-file", bodyFile,
		"--audit-db", dbPath,
	}, map[string]string{})
	if rc != 0 {
		t.Errorf("rc = %d; want 0", rc)
	}
	audit := extractEndAudit(t, stdout)
	if audit["audit_db"] != true {
		t.Errorf("audit_db = %v; want true (audit-db path was provided)", audit["audit_db"])
	}
}
