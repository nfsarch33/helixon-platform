package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout to a pipe and returns the read end, the
// write end, and a restore function. Caller should close pw after the call
// under test, then call restore to reset os.Stdout.
func captureStdout(t *testing.T) (r, pw *os.File, restore func()) {
	t.Helper()
	old := os.Stdout
	pr, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	restore = func() {
		os.Stdout = old
	}
	return pr, w, restore
}

func TestTail(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 100, "short"},
		{"", 100, ""},
		{"12345", 3, "...345"},
		{"12345", 10, "12345"},
	}
	for _, c := range cases {
		got := tail(c.in, c.max)
		if got != c.want {
			t.Errorf("tail(%q,%d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestRunVerify_BinaryMissing(t *testing.T) {
	t.Setenv("NOTIFY_FALLBACK", "/nonexistent/path/to/binary")
	rc := runVerify(verifyOptions{DryRun: true, Plan: "v_test", SentruxScore: 7000})
	if rc != 2 {
		t.Errorf("expected rc=2 for missing binary, got %d", rc)
	}
}

func TestRunVerify_BinaryMissing_NotDryRun(t *testing.T) {
	t.Setenv("NOTIFY_FALLBACK", "/nonexistent/path/to/binary")
	rc := runVerify(verifyOptions{DryRun: false, Plan: "v_test", SentruxScore: 7000})
	if rc != 2 {
		t.Errorf("expected rc=2 for missing binary (live mode), got %d", rc)
	}
}

func TestRunVerify_AuditJSON_Valid(t *testing.T) {
	t.Setenv("NOTIFY_FALLBACK", "/nonexistent/binary")
	r, pw, restore := captureStdout(t)
	defer restore()

	rc := runVerify(verifyOptions{DryRun: true, Plan: "v_audit_test", SentruxScore: 6944})
	_ = pw.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if rc != 2 {
		t.Errorf("expected rc=2, got %d", rc)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("audit event is not valid JSON: %v\nout=%s", err, out)
	}
	if parsed["result"] != "binary_missing" {
		t.Errorf("expected result=binary_missing, got %v", parsed["result"])
	}
	if parsed["plan"] != "v_audit_test" {
		t.Errorf("expected plan=v_audit_test, got %v", parsed["plan"])
	}
	if parsed["sentrux_score"] != float64(6944) {
		t.Errorf("expected sentrux_score=6944, got %v", parsed["sentrux_score"])
	}
}

func TestRunVerify_AuditContainsArgs(t *testing.T) {
	t.Setenv("NOTIFY_FALLBACK", "/nonexistent/binary")
	r, pw, restore := captureStdout(t)
	defer restore()

	_ = runVerify(verifyOptions{DryRun: true, Plan: "v_args", SentruxScore: 6944})
	_ = pw.Close()
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "--dry-run") {
		t.Errorf("expected --dry-run in args, got %s", out)
	}
	if !strings.Contains(string(out), "--plan") {
		t.Errorf("expected --plan in args, got %s", out)
	}
	if !strings.Contains(string(out), "v_args") {
		t.Errorf("expected plan value v_args, got %s", out)
	}
}

func TestRunVerify_AuditContainsSubject(t *testing.T) {
	t.Setenv("NOTIFY_FALLBACK", "/nonexistent/binary")
	r, pw, restore := captureStdout(t)
	defer restore()

	_ = runVerify(verifyOptions{DryRun: false, Plan: "v_nodry", SentruxScore: 6944})
	_ = pw.Close()
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "v_nodry") {
		t.Errorf("expected plan in subject, got %s", out)
	}
}

func TestRunVerify_SuccessPath(t *testing.T) {
	// /bin/true exits 0 with no output — perfect "successful binary" stand-in.
	t.Setenv("NOTIFY_FALLBACK", "/bin/true")
	r, pw, restore := captureStdout(t)
	defer restore()

	rc := runVerify(verifyOptions{DryRun: true, Plan: "v_ok", SentruxScore: 6944})
	_ = pw.Close()
	out, _ := io.ReadAll(r)

	if rc != 0 {
		t.Errorf("expected rc=0 for success, got %d (out=%s)", rc, out)
	}
	if !strings.Contains(string(out), `"result": "verified"`) {
		t.Errorf("expected result=verified, got %s", out)
	}
}

func TestRunVerify_VerifyFailed(t *testing.T) {
	// /bin/false exits non-zero — exercises the verify_failed branch.
	t.Setenv("NOTIFY_FALLBACK", "/bin/false")
	r, pw, restore := captureStdout(t)
	defer restore()

	rc := runVerify(verifyOptions{DryRun: true, Plan: "v_fail", SentruxScore: 6944})
	_ = pw.Close()
	out, _ := io.ReadAll(r)

	if rc != 1 {
		t.Errorf("expected rc=1 for failure, got %d", rc)
	}
	if !strings.Contains(string(out), `"result": "verify_failed"`) {
		t.Errorf("expected result=verify_failed, got %s", out)
	}
}

func TestInvokeBinary_Success(t *testing.T) {
	out, err := invokeBinary("/bin/echo", []string{"hello"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("expected hello in output, got %q", out)
	}
}

func TestInvokeBinary_Failure(t *testing.T) {
	_, err := invokeBinary("/bin/false", []string{})
	if err == nil {
		t.Error("expected error from /bin/false")
	}
}
