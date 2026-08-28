package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
)

// verifier_run END TO END, against a real container.
//
// Everything else in this package tests the argv, the check table and the
// result mapping. None of that catches the defect that mattered: the argv was
// right, the check table was right, the mapping was right, and every single
// invocation still came back pass:false because the container could not host
// the toolchain. An autonomy soak escalated ticket after ticket for it, and
// the tickets all read like the agent had written broken code.
//
// So these two tests exercise the whole path the agent actually walks —
// VerifierTool -> sandbox.Runner -> podman -> go -> JSON verdict — and assert
// BOTH directions: a green suite comes back pass:true, and a genuinely red one
// comes back pass:false with the test's own failure in the excerpt. The second
// is what stops the first from being satisfiable by a command that never ran.
//
// They share requirePodmanForVerifier with verifier_test.go: the skip is
// deliberately loud, for the same reason as the sandbox package's. A silent
// skip here restores the blind spot.
//
//	HELIXON_SANDBOX_REQUIRE_PODMAN=1   turn every skip into a failure
//	go test -short                     skip them (a container start costs
//	                                   70-130s on a vfs-backed rootless podman)

const (
	verifierITCommandTimeout = 5 * time.Minute
	verifierITCtxTimeout     = 6 * time.Minute
)

// verifierITWorkspace writes a minimal, dependency-free module. No
// dependencies is load-bearing: the sandbox has --network=none, so a module
// download would fail for reasons unrelated to what is under test.
func verifierITWorkspace(t *testing.T, failing bool) string {
	t.Helper()
	ws := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("go.mod", "module soak\n\ngo 1.24\n")
	write("soak.go", "package soak\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a + b }\n")
	want := "5"
	if failing {
		want = "6"
	}
	write("soak_test.go", "package soak\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n"+
		"\tif got := Add(2, 3); got != "+want+" {\n"+
		"\t\tt.Fatalf(\"Add(2, 3) = %d, want "+want+"\", got)\n\t}\n}\n")
	return ws
}

// runVerifierCheck drives the real tool handler and decodes its verdict.
func runVerifierCheck(t *testing.T, failing bool, check string) VerifierResult {
	t.Helper()
	requirePodmanForVerifier(t)
	runner, err := sandbox.NewRunner(sandbox.Config{
		Enabled:        true,
		Workspace:      verifierITWorkspace(t, failing),
		Timeout:        verifierITCommandTimeout,
		MaxOutputBytes: 16 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	tool := VerifierTool(VerifierConfig{Runner: runner, Timeout: verifierITCommandTimeout})
	ctx, cancel := context.WithTimeout(context.Background(), verifierITCtxTimeout)
	defer cancel()

	raw, err := tool.Handler(ctx, map[string]any{"check": check})
	if err != nil {
		t.Fatalf("verifier_run %s returned an error rather than a verdict: %v", check, err)
	}
	var got VerifierResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode verdict %q: %v", raw, err)
	}
	if got.Outcome == string(sandbox.OutcomeTimeout) {
		t.Fatalf("verifier_run %s TIMED OUT after %dms; this test asserts a verdict and a timeout is not one",
			check, got.DurationMS)
	}
	return got
}

// TestIT_VerifierRun_GoTestReportsAPass is the end-to-end positive control:
// the agent asks the verifier to prove its work, and the verifier can.
//
// MUTATION: revert any part of the fix (--userns=keep-id, HOME, GOCACHE,
// GOTMPDIR, or the EnsureWorkspaceScratch call) and this returns pass:false
// with a cache or permission error in the excerpt — which is exactly what the
// soak saw, and exactly what nothing in the suite noticed.
func TestIT_VerifierRun_GoTestReportsAPass(t *testing.T) {
	got := runVerifierCheck(t, false, "go_test")
	if !got.Pass {
		t.Fatalf("verifier_run go_test must PASS on a green module inside the sandbox; outcome=%s exit=%d\noutput:\n%s",
			got.Outcome, got.ExitCode, got.Output)
	}
	if got.Outcome != string(sandbox.OutcomePassed) {
		t.Fatalf("outcome = %q, want %q", got.Outcome, sandbox.OutcomePassed)
	}
}

// TestIT_VerifierRun_GoBuildReportsAPass: the compile path, end to end.
func TestIT_VerifierRun_GoBuildReportsAPass(t *testing.T) {
	got := runVerifierCheck(t, false, "go_build")
	if !got.Pass {
		t.Fatalf("verifier_run go_build must PASS on a compiling module; outcome=%s exit=%d\noutput:\n%s",
			got.Outcome, got.ExitCode, got.Output)
	}
}

// TestIT_VerifierRun_GoTestReportsAGenuineFailure keeps the test above
// honest. A verifier that reported pass:true for everything would satisfy the
// positive control alone; only a real red run distinguishes "the suite passed"
// from "nothing ran".
func TestIT_VerifierRun_GoTestReportsAGenuineFailure(t *testing.T) {
	got := runVerifierCheck(t, true, "go_test")
	if got.Pass {
		t.Fatalf("verifier_run go_test must NOT pass on a failing module; output:\n%s", got.Output)
	}
	if got.Outcome != string(sandbox.OutcomeFailed) {
		t.Fatalf("outcome = %q, want %q — a red suite is not the same fact as a sandbox error or a timeout",
			got.Outcome, sandbox.OutcomeFailed)
	}
	// And it must be the TEST that failed, not the sandbox. This is the
	// assertion that would have caught v18779: there, every check came back
	// failed too, but for reasons that had nothing to do with the code.
	if !strings.Contains(got.Output, "FAIL") || !strings.Contains(got.Output, "Add(2, 3)") {
		t.Fatalf("the excerpt must carry the test's own assertion failure so the agent can act on it; got:\n%s", got.Output)
	}
	for _, sandboxError := range []string{"read-only file system", "permission denied", "/nonexistent"} {
		if strings.Contains(strings.ToLower(got.Output), sandboxError) {
			t.Fatalf("the failure is the SANDBOX, not the code (%q):\n%s", sandboxError, got.Output)
		}
	}
}
