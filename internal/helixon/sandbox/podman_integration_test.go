package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The podman-backed tests below are the only ones that prove the container
// flags do what the argv says they do. Everything else in this package tests
// the argv, and an argv is not a boundary.
//
// The guard is LOUD by design. A suite that silently skips its only real
// containment assertions reports "0 failures" for a sandbox nobody has ever
// seen contain anything, so every skip prints why it skipped and says
// explicitly that the assertions did not run.
//
//	HLXN_SANDBOX_INTEGRATION=1         run them at all (they are OPT-IN)
//	HELIXON_SANDBOX_REQUIRE_PODMAN=1   run them AND turn every skip into a
//	                                   failure; implies the opt-in above
//	HELIXON_SANDBOX_TEST_IMAGE=<ref>   override the image under test
//
// THEY ARE OPT-IN, and the two variables above are the whole reason. This
// file already said "RUN THEM SEPARATELY — `go test ./...` starves the rest
// of the suite", but nothing enforced it, and CI's build-and-test step is
// exactly `go test -race ./...` with no -short. So all sixteen container
// tests in this package ran on every CI run and the package hit the 10m
// default deadline every time:
//
//	panic: test timed out after 10m0s
//	  TestIT_Podman_NetworkIsUnreachable (303.70s)
//
// A package that panics reports nothing about the other tests in it, so this
// suite was not merely slow: it permanently hid every regression the rest of
// the package could have caught, and a genuine break elsewhere would have
// looked exactly like the red everyone had learned to expect. Skipping by
// default and running these in a job of their own is what makes both signals
// real — see the sandbox-integration job in .github/workflows/ci.yml, which
// sets HELIXON_SANDBOX_REQUIRE_PODMAN=1 so that a skip there is a FAILURE.
// That job is the positive control for this gate: without it, "opt-in" would
// just be a longer way to spell "deleted".
//
// TIMING: one container start dominates, and it is a property of the host
// rather than of the image. TestIT_Podman_RunawayIsKilledAtTheTimeout alone
// spends 150s proving the wall clock is enforced, so no amount of per-host
// tuning makes the full set fit inside a 10m package budget beside
// everything else; the dedicated job carries its own -timeout instead. The
// per-command ceilings below stay generous on purpose. A tighter ceiling
// would turn an environment characteristic into a flaky security test — and
// a timeout that reads as "the write was blocked" is exactly the false pass
// these tests exist to avoid, which is why every assertion below checks the
// OUTCOME and not merely "did not pass".
//
//	go test ./...                                  # everything else; these skip
//	HELIXON_SANDBOX_REQUIRE_PODMAN=1 \
//	  go test ./internal/helixon/sandbox/ -run IT -timeout 40m

// integrationOptIn is the variable that has to be set for the container tests
// to run at all. It is deliberately NOT the same variable as the strict one:
// "run these" and "a skip is a failure" are different questions, and folding
// them together would leave no way to ask the first without the second.
const integrationOptIn = "HLXN_SANDBOX_INTEGRATION"

const (
	integrationCommandTimeout = 5 * time.Minute
	integrationCtxTimeout     = 6 * time.Minute
	// imageCheckTimeout bounds the `image exists` probe in the guard.
	//
	// It was 2 minutes, and that was not a generous ceiling — it was a
	// SILENT FAILURE SOURCE. The probe is a metadata lookup that measures
	// 0.54s on an idle host, but the engine's state database lives on this
	// host's slow substrate, and under the load this very suite generates
	// the probe was measured blowing straight past 120s. Its expiry was then
	// reported as "image is not present", so on a busy host the containment
	// assertions SKIPPED — quietly, and precisely when a long run had made
	// them most likely to matter. Five of them skipped that way in one
	// measured baseline run on main.
	//
	// The number is deliberately far above anything a working engine needs:
	// its job is to catch an engine that is wedged, not to referee one that
	// is merely busy.
	imageCheckTimeout = 5 * time.Minute
)

func integrationImage() string {
	if v := os.Getenv("HELIXON_SANDBOX_TEST_IMAGE"); v != "" {
		return v
	}
	return DefaultImage
}

func requirePodman(t *testing.T) {
	t.Helper()
	strict := os.Getenv("HELIXON_SANDBOX_REQUIRE_PODMAN") == "1"
	fail := func(format string, args ...any) {
		t.Helper()
		if strict {
			t.Fatalf("HELIXON_SANDBOX_REQUIRE_PODMAN=1: "+format, args...)
		}
		t.Skipf("SKIPPED — the podman containment assertions did NOT run: "+format, args...)
	}
	if !strict && os.Getenv(integrationOptIn) != "1" {
		fail("%s is not set; these are opt-in because the full set cannot fit the default 10m package deadline (see the file header)", integrationOptIn)
	}
	if testing.Short() && !strict {
		fail("-short was set; container start costs minutes on a rootless podman")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		fail("podman is not on PATH (%v); these tests are the only proof the container flags work", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageCheckTimeout)
	defer cancel()
	img := integrationImage()
	//nolint:gosec // G204 img comes from a constant or the test operator's own env
	err := exec.CommandContext(ctx, "podman", "image", "exists", img).Run()
	// A probe that ran out of time and a probe that answered "no" are
	// different facts about the host, and reporting the first as the second
	// sends the operator to `podman pull` for an image they already have.
	switch {
	case ctx.Err() != nil:
		fail("`podman image exists %s` did not answer within %s; the engine is unusable on this host, which is NOT the same as the image being absent", img, imageCheckTimeout)
	case err != nil:
		fail("image %q is not present (%v); run `podman pull %s`", img, err, img)
	}
}

func integrationRunner(t *testing.T, mutate func(Config) Config) (*Runner, context.Context) {
	t.Helper()
	requirePodman(t)
	cfg := Config{
		Enabled:        true,
		Workspace:      t.TempDir(),
		Image:          integrationImage(),
		Timeout:        integrationCommandTimeout,
		MaxOutputBytes: 8192,
	}.Normalize(t.TempDir())
	if mutate != nil {
		cfg = mutate(cfg)
	}
	r, err := NewRunner(cfg)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationCtxTimeout)
	t.Cleanup(cancel)
	return r, ctx
}

// mustRun runs spec and fails the test if the sandbox itself could not run.
// It returns the Result so the caller can assert on the OUTCOME, which is the
// difference between "the container refused this" and "the host was slow".
func mustRun(t *testing.T, r *Runner, ctx context.Context, spec Spec) Result {
	t.Helper()
	res, err := r.Run(ctx, spec)
	if err != nil {
		t.Fatalf("sandbox could not run %q: %v", spec.Command, err)
	}
	if res.Outcome == OutcomeTimeout {
		t.Fatalf("%q TIMED OUT after %dms — this test asserts a containment outcome, and a timeout is not one",
			spec.Command, res.DurationMS)
	}
	return res
}

// TestIT_Podman_NetworkIsUnreachable is the network-isolation proof: name
// resolution must FAIL (non-zero exit), not merely fail to finish.
//
// MUTATION: remove "--network=" + r.cfg.Network from Runner.EngineArgs — the
// container joins the default bridge, getent resolves, and this test fails.
func TestIT_Podman_NetworkIsUnreachable(t *testing.T) {
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.AllowedCommands = append(c.AllowedCommands, "getent")
		return c
	})
	res := mustRun(t, r, ctx, Spec{Command: "getent", Args: []string{"ahosts", "example.com"}})
	if res.Outcome != OutcomeFailed {
		t.Fatalf("name resolution outcome = %s (exit %d, output %q); with --network=none it must FAIL",
			res.Outcome, res.ExitCode, res.Output)
	}
}

// TestIT_Podman_RootFilesystemIsReadOnly proves --read-only.
//
// MUTATION: remove "--read-only" from EngineArgs and the touch succeeds.
func TestIT_Podman_RootFilesystemIsReadOnly(t *testing.T) {
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.AllowedCommands = append(c.AllowedCommands, "touch")
		return c
	})
	res := mustRun(t, r, ctx, Spec{Command: "touch", Args: []string{"/etc/helixon-escape"}})
	if res.Outcome != OutcomeFailed {
		t.Fatalf("touch /etc outcome = %s (exit %d): the container root is not read-only", res.Outcome, res.ExitCode)
	}
	if !strings.Contains(strings.ToLower(res.Output), "read-only") {
		t.Errorf("expected a read-only filesystem error; got %q", res.Output)
	}
}

// TestIT_Podman_WriteOutsideTheWorkspaceFails is the filesystem-isolation
// proof: a host directory that was not bind-mounted is not reachable, and
// nothing appears on the host.
//
// MUTATION: add a "--volume=/:/host:rw" bind to EngineArgs and this fails.
func TestIT_Podman_WriteOutsideTheWorkspaceFails(t *testing.T) {
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o777); err != nil { //nolint:gosec // G302 deliberately permissive so only isolation can block the write
		t.Fatalf("chmod: %v", err)
	}
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.AllowedCommands = append(c.AllowedCommands, "touch")
		return c
	})
	target := filepath.Join(outside, "escaped.txt")
	res := mustRun(t, r, ctx, Spec{Command: "touch", Args: []string{target}})
	if res.Outcome != OutcomeFailed {
		t.Fatalf("touch %q outcome = %s (exit %d, output %q): the host filesystem is reachable",
			target, res.Outcome, res.ExitCode, res.Output)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatalf("the sandbox created %q on the HOST filesystem", target)
	}
}

// TestIT_Podman_WorkspaceIsReachableAsNonRoot is the counterpart assertion:
// containment must not be achieved by making the sandbox useless. It also
// proves --user, since a root container would print uid 0.
func TestIT_Podman_WorkspaceIsReachableAsNonRoot(t *testing.T) {
	workspace := t.TempDir()
	// Rootless podman maps the container's uid to a subuid on the host, so
	// the mount has to be world-readable for a nobody-owned process.
	if err := os.Chmod(workspace, 0o777); err != nil { //nolint:gosec // G302 deliberately world-accessible test fixture
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("from the host"), 0o644); err != nil { //nolint:gosec // G306 test fixture
		t.Fatalf("write: %v", err)
	}
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.Workspace = workspace
		return c
	})
	res := mustRun(t, r, ctx, Spec{Command: "cat", Args: []string{DefaultWorkspaceMount + "/hello.txt"}})
	if res.Outcome != OutcomePassed || !strings.Contains(res.Output, "from the host") {
		t.Fatalf("the workspace must be readable inside the sandbox; outcome=%s output=%q", res.Outcome, res.Output)
	}
}

// TestIT_Podman_RunsAsNonRoot proves --user against a real container.
//
// MUTATION: remove "--user=" from EngineArgs — the golang image's default
// user is root, so id -u prints 0 and this fails.
func TestIT_Podman_RunsAsNonRoot(t *testing.T) {
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.AllowedCommands = append(c.AllowedCommands, "id")
		return c
	})
	res := mustRun(t, r, ctx, Spec{Command: "id", Args: []string{"-u"}})
	if res.Outcome != OutcomePassed {
		t.Fatalf("id -u outcome = %s: %s", res.Outcome, res.Output)
	}
	if strings.TrimSpace(res.Output) == "0" {
		t.Fatal("the sandbox is running as root")
	}
}

// TestIT_Podman_OutputIsTruncatedAtTheCap proves the bound against a process
// that genuinely produces megabytes, and — the subtle half — that truncation
// does not kill the child. A short write from the output writer reaches
// os/exec, which tears the pipe down and EPIPEs the process.
//
// MUTATION: return the retained count instead of len(p) from
// BoundedBuffer.Write and this test fails with a non-zero exit.
func TestIT_Podman_OutputIsTruncatedAtTheCap(t *testing.T) {
	const capBytes = 2048
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.MaxOutputBytes = capBytes
		return c
	})
	res := mustRun(t, r, ctx, Spec{Command: "head", Args: []string{"-c", "4194304", "/dev/zero"}})
	if len(res.Output) != capBytes {
		t.Fatalf("retained %d bytes, want the %d-byte cap", len(res.Output), capBytes)
	}
	if !res.Truncated {
		t.Fatal("Truncated must be reported so the model knows the excerpt is partial")
	}
	if res.OutputSize <= capBytes {
		t.Fatalf("OutputSize = %d; the full byte count must be recorded", res.OutputSize)
	}
	if res.Outcome != OutcomePassed {
		t.Fatalf("the child must finish cleanly despite truncation (no EPIPE); outcome=%s exit=%d output-tail=%q",
			res.Outcome, res.ExitCode, res.Output[max(0, len(res.Output)-120):])
	}
}

// TestIT_Podman_RunawayIsKilledAtTheTimeout proves the hard wall clock.
//
// MUTATION: drop the context.WithTimeout in Runner.Run and this test hangs
// until the go test deadline instead of returning OutcomeTimeout.
func TestIT_Podman_RunawayIsKilledAtTheTimeout(t *testing.T) {
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.AllowedCommands = append(c.AllowedCommands, "sleep")
		return c
	})
	// The ceiling has to exceed container-start cost on this host, or the
	// test would pass for the wrong reason (start slower than the timeout).
	const ceiling = 150 * time.Second
	started := time.Now()
	res, err := r.Run(ctx, Spec{Command: "sleep", Args: []string{"3600"}, Timeout: ceiling})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("a timeout is a Result, not an error: %v", err)
	}
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("outcome = %s after %s, want %s (output=%q)", res.Outcome, elapsed, OutcomeTimeout, res.Output)
	}
	if res.Pass() {
		t.Fatal("a timed-out run must not report Pass()")
	}
	if elapsed > ceiling+90*time.Second {
		t.Fatalf("the runaway took %s to die against a %s ceiling; the timeout is not being enforced", elapsed, ceiling)
	}
}

// TestIT_Podman_MissingImageRefusesToStart proves the sandbox fails loudly
// rather than degrading, against a real engine. It starts no container, so it
// is fast.
func TestIT_Podman_MissingImageRefusesToStart(t *testing.T) {
	r, ctx := integrationRunner(t, func(c Config) Config {
		c.Image = "localhost/helixon-nonexistent-v18779:absent"
		return c
	})
	res, err := r.Run(ctx, Spec{Command: "echo", Args: []string{"hi"}})
	if !errors.Is(err, ErrImageMissing) {
		t.Fatalf("a missing image must return ErrImageMissing, got %v", err)
	}
	if res.Output != "" {
		t.Fatalf("nothing may have executed; got output %q", res.Output)
	}
	if !strings.Contains(err.Error(), "Refusing to fall back to host execution") {
		t.Fatalf("the error must state the refusal explicitly; got %q", err)
	}
}
