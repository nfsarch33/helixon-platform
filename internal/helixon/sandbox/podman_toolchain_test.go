package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// POSITIVE CONTROLS.
//
// podman_integration_test.go proves the sandbox BLOCKS things. Every one of
// its eight assertions is of the form "this was refused". That is a complete
// set of negative controls and an empty set of positive ones, and a suite
// shaped like that cannot tell containment from paralysis: a sandbox that
// blocks the network, the root filesystem, the host, AND the Go toolchain it
// exists to host passes all eight and is useless.
//
// That is not hypothetical. v18779 shipped exactly that sandbox. Every `go`
// check the agent ran inside it died on one of three things — a build cache
// under a HOME of /nonexistent on a read-only root, a test binary linked into
// a noexec tmpfs, and a bind mount owned by a uid the container was not — and
// an autonomy soak escalated ticket after ticket that all looked like the
// agent could not write working code.
//
// The tests below are the missing half: they assert that legitimate work
// SUCCEEDS inside the fully-hardened sandbox. MEASURED mutation results, not
// guesses — each line was run:
//
//	--userns=keep-id -> --user=65534:65534   KILLED. "open /workspace/go.mod:
//	                                         permission denied" — the v18779
//	                                         symptom, exactly.
//	GOTMPDIR -> /tmp (the noexec tmpfs)      KILLED. "fork/exec
//	                                         /tmp/.../soak.test: permission
//	                                         denied".
//	remove the EnsureWorkspaceScratch call   KILLED. "creating work dir: stat
//	                                         /workspace/.gotmp: no such file
//	                                         or directory".
//	drop HOME alone                          SURVIVES. GOCACHE and GOPATH are
//	                                         explicit, so nothing moves.
//	drop GOCACHE and GOPATH alone            SURVIVES. Both default under
//	                                         HOME=/tmp.
//	drop HOME + GOCACHE + GOPATH together    KILLED — but by
//	                                         TestIT_Podman_CachesStayOutOfTheWorkspace,
//	                                         NOT by a build failure. Under
//	                                         keep-id the toolchain then puts
//	                                         its cache in the agent's own
//	                                         repository and builds happily.
//	                                         See Config.DefaultToolchainEnv.
//
// They share the guard, the timing characteristics and the run instructions
// documented at the top of podman_integration_test.go — including the LOUD
// skip, which matters more here than anywhere: a silent skip would restore
// precisely the blind spot this file exists to close.

// goModule is a minimal, dependency-free module. No dependencies is
// load-bearing: the container has --network=none, so anything that needed a
// module download would fail for a reason that has nothing to do with the
// sandbox flags under test.
type goModule struct {
	// failing makes the test in the module assert something untrue, so a
	// "pass" cannot simply mean the command never ran.
	failing bool
	// unformatted adds a file gofmt will report, for the same reason.
	unformatted bool
}

func writeGoModule(t *testing.T, mod goModule) string {
	t.Helper()
	// Fresh and EMPTY, exactly like a newly provisioned agent workspace: the
	// scratch directory GOTMPDIR points at does not exist yet, and the first
	// run has to work anyway.
	ws := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// The go directive is deliberately older than the image's toolchain so no
	// toolchain switch is attempted; a network-less container cannot download
	// one. See GOTOOLCHAIN in Config.DefaultToolchainEnv.
	write("go.mod", "module soak\n\ngo 1.24\n")
	write("soak.go", "package soak\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a + b }\n")
	want := "5"
	if mod.failing {
		want = "6"
	}
	write("soak_test.go", "package soak\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n"+
		"\tif got := Add(2, 3); got != "+want+" {\n"+
		"\t\tt.Fatalf(\"Add(2, 3) = %d, want "+want+"\", got)\n\t}\n}\n")
	if mod.unformatted {
		write("ragged.go", "package soak\n\n\n\nfunc   Sub( a,b int )   int {\n\t\t\treturn a-b\n}\n")
	}
	return ws
}

// Every test below runs with every hardening control at its default. Nothing
// here relaxes a boundary; the only thing that changed between v18779 and now
// is that the container can reach its own workspace and its own caches.

// TestIT_Podman_ToolchainCanCompile: `go build ./...` succeeds inside the
// fully-hardened sandbox. This is the single most basic thing the sandbox
// exists to do, and before this change it could not do it.
func TestIT_Podman_ToolchainCanCompile(t *testing.T) {
	ws := writeGoModule(t, goModule{})
	r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
	res := mustRun(t, r, ctx, Spec{Command: "go", Args: []string{"build", "./..."}, SkipAllowList: true})
	if res.Outcome != OutcomePassed {
		t.Fatalf("go build ./... outcome = %s (exit %d) inside the sandbox; a sandbox that cannot compile is not hardened, it is broken.\noutput:\n%s",
			res.Outcome, res.ExitCode, res.Output)
	}
}

// TestIT_Podman_ToolchainPassingTestPasses: a green suite is reported green.
func TestIT_Podman_ToolchainPassingTestPasses(t *testing.T) {
	ws := writeGoModule(t, goModule{})
	r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
	res := mustRun(t, r, ctx, Spec{Command: "go", Args: []string{"test", "./..."}, SkipAllowList: true})
	if res.Outcome != OutcomePassed {
		t.Fatalf("go test ./... outcome = %s (exit %d); the sandbox must be able to build AND EXECUTE a test binary.\noutput:\n%s",
			res.Outcome, res.ExitCode, res.Output)
	}
	if !strings.Contains(res.Output, "ok") {
		t.Fatalf("expected `ok` from a passing suite; got %q", res.Output)
	}
	// The test binary is linked into GOTMPDIR and then executed. If GOTMPDIR
	// were on the noexec tmpfs this would read "permission denied" instead.
	if strings.Contains(res.Output, "permission denied") {
		t.Fatalf("GOTMPDIR is not exec-capable: %s", res.Output)
	}
}

// TestIT_Podman_ToolchainFailingTestFails is what makes the test above mean
// something. Without it, "passed" is equally consistent with "the suite ran
// and was green" and "the command silently did nothing" — and the second is
// exactly the failure mode a positive control has to rule out.
func TestIT_Podman_ToolchainFailingTestFails(t *testing.T) {
	ws := writeGoModule(t, goModule{failing: true})
	r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
	res := mustRun(t, r, ctx, Spec{Command: "go", Args: []string{"test", "./..."}, SkipAllowList: true})
	if res.Outcome != OutcomeFailed {
		t.Fatalf("a genuinely failing test must be reported as %s, got %s (exit %d).\noutput:\n%s",
			OutcomeFailed, res.Outcome, res.ExitCode, res.Output)
	}
	// And it must fail for the RIGHT reason: the assertion, not the sandbox.
	if !strings.Contains(res.Output, "FAIL") || !strings.Contains(res.Output, "Add(2, 3)") {
		t.Fatalf("the failure must be the test's own assertion, not a sandbox error; output:\n%s", res.Output)
	}
}

// TestIT_Podman_ToolchainVetRuns proves `go vet` — which needs the same build
// cache as everything else — completes inside the sandbox.
func TestIT_Podman_ToolchainVetRuns(t *testing.T) {
	ws := writeGoModule(t, goModule{})
	r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
	res := mustRun(t, r, ctx, Spec{Command: "go", Args: []string{"vet", "./..."}, SkipAllowList: true})
	if res.Outcome != OutcomePassed {
		t.Fatalf("go vet ./... outcome = %s (exit %d).\noutput:\n%s", res.Outcome, res.ExitCode, res.Output)
	}
}

// TestIT_Podman_GofmtRuns checks both directions in one test, because
// `gofmt -l` exits zero either way: a clean tree must print nothing, and a
// ragged one must name the file. The second half is the proof gofmt actually
// inspected the workspace rather than finding an empty one.
func TestIT_Podman_GofmtRuns(t *testing.T) {
	t.Run("clean tree lists nothing", func(t *testing.T) {
		ws := writeGoModule(t, goModule{})
		r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
		res := mustRun(t, r, ctx, Spec{Command: "gofmt", Args: []string{"-l", "."}, SkipAllowList: true})
		if res.Outcome != OutcomePassed {
			t.Fatalf("gofmt -l outcome = %s (exit %d): %s", res.Outcome, res.ExitCode, res.Output)
		}
		if strings.TrimSpace(res.Output) != "" {
			t.Fatalf("a formatted tree must produce no output; got %q", res.Output)
		}
	})
	t.Run("ragged file is listed", func(t *testing.T) {
		ws := writeGoModule(t, goModule{unformatted: true})
		r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
		res := mustRun(t, r, ctx, Spec{Command: "gofmt", Args: []string{"-l", "."}, SkipAllowList: true})
		if res.Outcome != OutcomePassed {
			t.Fatalf("gofmt -l outcome = %s (exit %d): %s", res.Outcome, res.ExitCode, res.Output)
		}
		if !strings.Contains(res.Output, "ragged.go") {
			t.Fatalf("gofmt must name the unformatted file, otherwise a clean result proves nothing; got %q", res.Output)
		}
	})
}

// TestIT_Podman_ScratchDirIsCreatedInAFreshWorkspace proves the GOTMPDIR
// directory is materialized on the HOST side of the bind mount by the run
// itself. The go tool never creates GOTMPDIR, so without this the first run in
// every new workspace would fail.
func TestIT_Podman_ScratchDirIsCreatedInAFreshWorkspace(t *testing.T) {
	ws := writeGoModule(t, goModule{})
	scratch := filepath.Join(ws, WorkspaceScratchDir)
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("precondition: the workspace must start without %s (%v)", WorkspaceScratchDir, err)
	}
	r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
	res := mustRun(t, r, ctx, Spec{Command: "go", Args: []string{"test", "./..."}, SkipAllowList: true})
	if res.Outcome != OutcomePassed {
		t.Fatalf("a fresh, empty workspace must work on its FIRST run; outcome=%s output:\n%s", res.Outcome, res.Output)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("%s was not created for GOTMPDIR: %v", scratch, err)
	}
}

// TestIT_Podman_CachesStayOutOfTheWorkspace is the positive control that the
// GOCACHE/GOPATH/HOME defaults actually need.
//
// Removing them does not break the build — under keep-id the Go toolchain
// simply resolves GOCACHE to /workspace/.cache/go-build and carries on. That
// is a worse failure than a loud one: the agent's repository silently acquires
// a build cache, which shows up in its own file listings, in `git status`, and
// in whatever diff it proposes. So the assertion is about what the run LEAVES
// BEHIND, not about whether it succeeded.
//
// MUTATION: delete HOME, GOCACHE and GOPATH from DefaultToolchainEnv together
// and this test fails on a stray .cache directory. (Deleting any one of them
// alone changes nothing; see the header.)
func TestIT_Podman_CachesStayOutOfTheWorkspace(t *testing.T) {
	ws := writeGoModule(t, goModule{})
	before, err := os.ReadDir(ws)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })
	res := mustRun(t, r, ctx, Spec{Command: "go", Args: []string{"test", "./..."}, SkipAllowList: true})
	if res.Outcome != OutcomePassed {
		t.Fatalf("precondition: the run must succeed; outcome=%s output:\n%s", res.Outcome, res.Output)
	}

	permitted := map[string]bool{WorkspaceScratchDir: true}
	for _, e := range before {
		permitted[e.Name()] = true
	}
	after, err := os.ReadDir(ws)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range after {
		if !permitted[e.Name()] {
			t.Errorf("the run left %q in the agent's workspace; caches belong on the scratch tmpfs, "+
				"not in the repository the agent is reasoning about", e.Name())
		}
	}
}

// TestIT_Podman_V18779FlagsCannotRunTheToolchain is the regression witness: it
// reconstructs the pre-fix configuration and asserts it is BROKEN.
//
// It is here so the reasoning cannot quietly rot back. If someone later
// "simplifies" the sandbox by restoring --user and dropping the env defaults,
// the positive controls above go red — and this test says, in one place, what
// exactly they would be restoring.
func TestIT_Podman_V18779FlagsCannotRunTheToolchain(t *testing.T) {
	ws := writeGoModule(t, goModule{})
	r, ctx := integrationRunner(t, func(c Config) Config { c.Workspace = ws; return c })

	legacy := r.cfg
	legacy.UserNS = UserNSDisabled // the v18779 shape: a fixed uid...
	legacy.User = DefaultUser
	legacy.Env = nil // ...and no toolchain environment at all
	validated, err := legacy.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	broken := &Runner{cfg: validated, engine: osEngine{}}

	res := mustRun(t, broken, ctx, Spec{Command: "go", Args: []string{"test", "./..."}, SkipAllowList: true})
	if res.Outcome == OutcomePassed {
		t.Fatalf("the v18779 flag set is expected to be UNUSABLE; if it now works, this witness and the "+
			"keep-id/env defaults it justifies should be re-examined rather than left in place.\noutput:\n%s", res.Output)
	}
	lower := strings.ToLower(res.Output)
	if !strings.Contains(lower, "read-only file system") && !strings.Contains(lower, "permission denied") {
		t.Fatalf("expected the documented cache/permission failure; got:\n%s", res.Output)
	}
}
