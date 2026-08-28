package builtins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
)

func TestBuildVerifierArgv_TableDriven(t *testing.T) {
	t.Parallel()
	checks := map[string]VerifierCheck{}
	for _, c := range DefaultVerifierChecks() {
		checks[c.Name] = c
	}

	tests := []struct {
		name    string
		check   VerifierCheck
		extra   []string
		want    []string
		wantErr string
	}{
		{name: "go_test with no extras", check: checks["go_test"], want: []string{"test", "./..."}},
		{name: "go_test with a package pattern", check: checks["go_test"], extra: []string{"./internal/..."}, want: []string{"test", "./...", "./internal/..."}},
		{name: "gofmt_check refuses extras", check: checks["gofmt_check"], extra: []string{"."}, wantErr: "does not accept extra arguments"},
		{
			// -toolexec runs an arbitrary binary for every compile; a check
			// that accepted "extra arguments" without this rule would be a
			// shell in disguise.
			name: "a flag-shaped extra argument is refused", check: checks["go_test"],
			extra: []string{"-toolexec=/bin/sh"}, wantErr: "may not be a flag",
		},
		{name: "file_exists needs a path", check: checks["file_exists"], wantErr: "requires at least 1 argument"},
		{name: "file_contains needs two arguments", check: checks["file_contains"], extra: []string{"pattern"}, wantErr: "requires at least 2 argument"},
		{
			name: "file_exists rejects a path outside the workspace mount", check: checks["file_exists"],
			extra: []string{"/etc/passwd"}, wantErr: "resolves outside",
		},
		{
			name: "file_exists accepts a workspace path", check: checks["file_exists"],
			extra: []string{sandbox.DefaultWorkspaceMount + "/go.mod"},
			want:  []string{"-e", sandbox.DefaultWorkspaceMount + "/go.mod"},
		},
		{
			name: "file_exists rejects traversal out of the workspace mount", check: checks["file_exists"],
			extra: []string{sandbox.DefaultWorkspaceMount + "/../etc/passwd"}, wantErr: "resolves outside",
		},
		{
			name:  "file_contains passes pattern and path through",
			check: checks["file_contains"], extra: []string{"module", "go.mod"},
			want: []string{"-q", "--", "module", "go.mod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildVerifierArgv(tt.check, append([]string(nil), tt.extra...), sandbox.DefaultWorkspaceMount)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("buildVerifierArgv() = %v, want an error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildVerifierArgv: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("argv = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifierExtraArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    map[string]any
		want    []string
		wantErr string
	}{
		{name: "absent", args: map[string]any{}},
		{name: "explicit nil", args: map[string]any{"args": nil}},
		{name: "strings", args: map[string]any{"args": []any{"a", "b"}}, want: []string{"a", "b"}},
		{name: "not an array", args: map[string]any{"args": "a"}, wantErr: "must be an array"},
		{name: "non-string entry", args: map[string]any{"args": []any{1}}, wantErr: "must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := verifierExtraArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("verifierExtraArgs() = %v, want an error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifierExtraArgs: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifierTool_SchemaAndGuards(t *testing.T) {
	t.Parallel()
	def := VerifierTool(VerifierConfig{})
	if def.Name != VerifierToolName {
		t.Fatalf("name = %q, want %q", def.Name, VerifierToolName)
	}
	var schema map[string]any
	if err := json.Unmarshal(def.Parameters, &schema); err != nil {
		t.Fatalf("parameters are not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
	for _, name := range []string{"go_build", "go_test", "go_vet"} {
		if !strings.Contains(def.Description, name) {
			t.Errorf("description must list the %q check so the model knows it exists", name)
		}
	}

	ctx := context.Background()
	// No runner: the verifier must refuse rather than shelling out on the
	// host, which would hand the agent a second unsandboxed primitive.
	if _, err := def.Handler(ctx, map[string]any{"check": "go_test"}); err == nil ||
		!strings.Contains(err.Error(), "never executes on the host") {
		t.Fatalf("a verifier with no sandbox must refuse; got %v", err)
	}

	withRunner := VerifierTool(VerifierConfig{Runner: &sandbox.Runner{}})
	if _, err := withRunner.Handler(ctx, map[string]any{"check": "rm_rf"}); err == nil ||
		!strings.Contains(err.Error(), "unknown check") {
		t.Fatalf("an unlisted check must be refused; got %v", err)
	}
}

// TestEncodeVerifierResult is the mapping the completion gate depends on: a
// verdict that says pass:true for anything other than a clean exit would let
// a red or unfinished run report itself complete.
func TestEncodeVerifierResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		res           sandbox.Result
		maxBytes      int
		wantPass      bool
		wantOutcome   string
		wantTruncated bool
		wantOutput    string
	}{
		{
			name:        "clean exit passes",
			res:         sandbox.Result{Outcome: sandbox.OutcomePassed, Output: "ok", Command: "go test"},
			maxBytes:    100,
			wantPass:    true,
			wantOutcome: "passed",
			wantOutput:  "ok",
		},
		{
			name:        "non-zero exit fails",
			res:         sandbox.Result{Outcome: sandbox.OutcomeFailed, ExitCode: 1, Output: "FAIL"},
			maxBytes:    100,
			wantOutcome: "failed",
			wantOutput:  "FAIL",
		},
		{
			name:        "timeout is not a failure and not a pass",
			res:         sandbox.Result{Outcome: sandbox.OutcomeTimeout, ExitCode: -1},
			maxBytes:    100,
			wantOutcome: "timeout",
		},
		{
			name:        "sandbox error",
			res:         sandbox.Result{Outcome: sandbox.OutcomeError, ExitCode: -1},
			maxBytes:    100,
			wantOutcome: "error",
		},
		{
			name:          "sandbox already truncated",
			res:           sandbox.Result{Outcome: sandbox.OutcomePassed, Output: "abc", Truncated: true, OutputSize: 99999},
			maxBytes:      100,
			wantPass:      true,
			wantOutcome:   "passed",
			wantTruncated: true,
			wantOutput:    "abc",
		},
		{
			name:          "excerpt cap truncates again",
			res:           sandbox.Result{Outcome: sandbox.OutcomePassed, Output: strings.Repeat("x", 50)},
			maxBytes:      10,
			wantPass:      true,
			wantOutcome:   "passed",
			wantTruncated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := encodeVerifierResult("go_test", tt.res, tt.maxBytes)
			if err != nil {
				t.Fatalf("encodeVerifierResult: %v", err)
			}
			var got VerifierResult
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("result is not valid JSON (%q): %v", raw, err)
			}
			if got.Pass != tt.wantPass {
				t.Errorf("pass = %v, want %v", got.Pass, tt.wantPass)
			}
			if got.Outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, tt.wantOutcome)
			}
			if got.Truncated != tt.wantTruncated {
				t.Errorf("truncated = %v, want %v", got.Truncated, tt.wantTruncated)
			}
			if tt.wantOutput != "" && got.Output != tt.wantOutput {
				t.Errorf("output = %q, want %q", got.Output, tt.wantOutput)
			}
			if got.Check != "go_test" {
				t.Errorf("check = %q", got.Check)
			}
		})
	}
}

// TestVerifierTool_SandboxFailureIsAnError proves the verifier surfaces a
// sandbox that cannot run as an ERROR rather than as a verdict. A missing
// image coming back as pass:false would be indistinguishable from a genuinely
// failing check, and a missing image coming back as pass:true would let a run
// claim evidence it never produced.
//
// No container starts: the preflight refuses before anything executes.
func TestVerifierTool_SandboxFailureIsAnError(t *testing.T) {
	t.Parallel()
	runner, err := sandbox.NewRunner(sandbox.Config{
		Enabled:   true,
		Workspace: t.TempDir(),
		Image:     "localhost/helixon-absent-v18779:none",
		Timeout:   30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	def := VerifierTool(VerifierConfig{Runner: runner})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := def.Handler(ctx, map[string]any{"check": "go_vet"})
	if err == nil {
		t.Fatalf("a sandbox that cannot run must be an error, not a verdict; got %q", out)
	}
	if !strings.Contains(err.Error(), "verifier_run go_vet") {
		t.Errorf("the error must name the check; got %v", err)
	}
}

func TestMemoryHelpers_Defaults(t *testing.T) {
	t.Parallel()
	app, user := memoryAppUserID(map[string]any{}, "app-default", "user-default")
	if app != "app-default" || user != "user-default" {
		t.Errorf("defaults not applied: %q %q", app, user)
	}
	app, user = memoryAppUserID(map[string]any{"app_id": "a", "user_id": "u"}, "app-default", "user-default")
	if app != "a" || user != "u" {
		t.Errorf("overrides not applied: %q %q", app, user)
	}
	if got := memoryTenantID(map[string]any{}, "t-default"); got != "t-default" {
		t.Errorf("tenant default not applied: %q", got)
	}
	if got := memoryTenantID(map[string]any{"tenant_id": "t1"}, "t-default"); got != "t1" {
		t.Errorf("tenant override not applied: %q", got)
	}
}

func TestMemoryTool_UnconfiguredAndUnknownOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unconfigured := MemoryTool(nil, "app", "user", "tenant")
	if _, err := unconfigured.Handler(ctx, map[string]any{"op": "read"}); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("an unconfigured memory tool must refuse; got %v", err)
	}
}

func TestVerifierConfig_Defaults(t *testing.T) {
	t.Parallel()
	got := VerifierConfig{}.withDefaults()
	if len(got.Checks) != len(DefaultVerifierChecks()) {
		t.Fatalf("checks = %d, want the default table", len(got.Checks))
	}
	if got.Timeout <= 0 || got.MaxOutputBytes <= 0 {
		t.Fatalf("defaults not applied: %+v", got)
	}
}

// --- podman-backed ---------------------------------------------------------

// requirePodmanForVerifier mirrors the guard in internal/helixon/sandbox: the
// skip is loud, because a verifier that has never been shown to run a real
// container proves nothing about anyone's work.
func requirePodmanForVerifier(t *testing.T) {
	t.Helper()
	strict := os.Getenv("HELIXON_SANDBOX_REQUIRE_PODMAN") == "1"
	fail := func(format string, args ...any) {
		t.Helper()
		if strict {
			t.Fatalf("HELIXON_SANDBOX_REQUIRE_PODMAN=1: "+format, args...)
		}
		t.Skipf("SKIPPED — the verifier sandbox assertions did NOT run: "+format, args...)
	}
	if testing.Short() && !strict {
		fail("-short was set; container start costs minutes on a vfs-backed rootless podman")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		fail("podman is not on PATH: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	//nolint:gosec // G204 the image reference is a package constant, not input
	if err := exec.CommandContext(ctx, "podman", "image", "exists", sandbox.DefaultImage).Run(); err != nil {
		fail("image %q is not present: %v", sandbox.DefaultImage, err)
	}
}

// TestIT_VerifierTool_PassAndFailAreStructured proves the agent gets a
// readable verdict for both outcomes, from a real container.
func TestIT_VerifierTool_PassAndFailAreStructured(t *testing.T) {
	requirePodmanForVerifier(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "present.txt"), []byte("hi"), 0o644); err != nil { //nolint:gosec // G306 test fixture
		t.Fatalf("write: %v", err)
	}
	// Generous ceilings: podman's storage driver on win1/wsl1 is vfs, so a
	// container start costs minutes. A tight ceiling here would report
	// outcome "timeout" and tell us nothing about the verifier.
	runner, err := sandbox.NewRunner(sandbox.Config{
		Enabled: true, Workspace: workspace, Timeout: 6 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	def := VerifierTool(VerifierConfig{Runner: runner, Timeout: 5 * time.Minute})
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()

	tests := []struct {
		name     string
		path     string
		wantPass bool
		wantOut  string
	}{
		{name: "existing file passes", path: sandbox.DefaultWorkspaceMount + "/present.txt", wantPass: true, wantOut: "passed"},
		{name: "missing file fails", path: sandbox.DefaultWorkspaceMount + "/absent.txt", wantPass: false, wantOut: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, hErr := def.Handler(ctx, map[string]any{"check": "file_exists", "args": []any{tt.path}})
			if hErr != nil {
				t.Fatalf("a failing check must be a RESULT, not an error: %v", hErr)
			}
			var res VerifierResult
			if err := json.Unmarshal([]byte(raw), &res); err != nil {
				t.Fatalf("result is not valid JSON (%q): %v", raw, err)
			}
			if res.Pass != tt.wantPass {
				t.Errorf("pass = %v, want %v (raw=%s)", res.Pass, tt.wantPass, raw)
			}
			if res.Outcome != tt.wantOut {
				t.Errorf("outcome = %q, want %q", res.Outcome, tt.wantOut)
			}
			if res.Check != "file_exists" {
				t.Errorf("check = %q", res.Check)
			}
			if res.DurationMS < 0 {
				t.Errorf("duration_ms = %d", res.DurationMS)
			}
		})
	}
}
