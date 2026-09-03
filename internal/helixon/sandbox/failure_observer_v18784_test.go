package sandbox

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// observedKinds runs spec against r and returns what the failure observer saw.
func observedKinds(t *testing.T, r *Runner, spec Spec) []string {
	t.Helper()
	var seen []string
	r.SetFailureObserver(func(kind string) { seen = append(seen, kind) })
	_, _ = r.Run(context.Background(), spec)
	return seen
}

// TestFailureObserverSeesRejections: the sandbox refusing the command the agent
// asked for is CONTAINMENT WORKING, and reports `rejected`.
//
// This used to assert `preflight` alongside the outage cases below, on the
// reasoning that both stop the command before the container starts. Production
// disproved it: every observed increment was the model emitting a shell
// pipeline, ValidateArgv refusing it correctly, and an operator being paged to
// look for a broken sandbox that was working.
func TestFailureObserverSeesRejections(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (*Runner, Spec)
	}{
		{
			name: "rejected argv",
			setup: func(t *testing.T) (*Runner, Spec) {
				r, _ := newTestRunner(t, nil)
				// A path, not a bare binary name: ValidateArgv refuses it
				// before anything is started.
				return r, Spec{Command: "/bin/sh", Args: []string{"-c", "id"}}
			},
		},
		{
			name: "shell pipeline as the command",
			setup: func(t *testing.T) (*Runner, Spec) {
				r, _ := newTestRunner(t, nil)
				// Verbatim shape of what the live agent sent three times on
				// 2026-08-29 and what mislabelled the metric.
				return r, Spec{Command: "ls -la /workspace/ 2>&1 | head -50"}
			},
		},
		{
			name: "command outside the allow-list",
			setup: func(t *testing.T) (*Runner, Spec) {
				r, _ := newTestRunner(t, func(c Config) Config {
					c.AllowedCommands = []string{"go"}
					return c
				})
				return r, Spec{Command: "curl", Args: []string{"example.com"}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, spec := tc.setup(t)
			seen := observedKinds(t, r, spec)
			if len(seen) != 1 || seen[0] != FailureKindRejected {
				t.Fatalf("observer saw %v, want exactly [%s]; a refusal is the boundary working, not an outage",
					seen, FailureKindRejected)
			}
		})
	}
}

// TestFailureObserverSeesPreflightOutages: the sandbox unable to start ANY
// command is a genuine host or config problem, and reports `preflight`.
func TestFailureObserverSeesPreflightOutages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (*Runner, Spec)
	}{
		{
			name: "engine missing",
			setup: func(t *testing.T) (*Runner, Spec) {
				r, fe := newTestRunner(t, nil)
				fe.lookErr = errors.New("podman: not found")
				return r, Spec{Command: "echo", Args: []string{"hi"}}
			},
		},
		{
			name: "image missing",
			setup: func(t *testing.T) (*Runner, Spec) {
				r, fe := newTestRunner(t, nil)
				fe.imageErr = errors.New("no such image")
				return r, Spec{Command: "echo", Args: []string{"hi"}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, spec := tc.setup(t)
			seen := observedKinds(t, r, spec)
			if len(seen) != 1 || seen[0] != FailureKindPreflight {
				t.Fatalf("observer saw %v, want exactly [%s]", seen, FailureKindPreflight)
			}
		})
	}
}

// TestRejectionAndOutageNeverShareALabel is the guard on the whole split.
//
// It fails if anyone merges the two kinds back together — which is the natural
// simplification to reach for, and the one that produced the false page. An
// alert keyed on the outage kind must stay silent while the agent is being
// refused, and that is only true while these two labels differ.
func TestRejectionAndOutageNeverShareALabel(t *testing.T) {
	t.Parallel()
	if FailureKindRejected == FailureKindPreflight {
		t.Fatal("rejected and preflight are the same label; an alert on sandbox outages will now fire " +
			"every time containment correctly refuses the agent, which is the exact false page this split fixed")
	}

	// And prove it end to end rather than by string comparison alone: a refusal
	// must emit nothing that an outage alert would match.
	r, _ := newTestRunner(t, nil)
	refusal := observedKinds(t, r, Spec{Command: "/bin/sh", Args: []string{"-c", "id"}})
	for _, k := range refusal {
		if k == FailureKindPreflight || k == FailureKindExec || k == FailureKindTimeout {
			t.Fatalf("a refused command emitted %q, which the outage alert watches", k)
		}
	}
	if len(refusal) == 0 {
		t.Fatal("a refused command emitted nothing at all; the rejection signal has been lost entirely")
	}
}

func TestFailureObserverSeesTimeout(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, func(c Config) Config {
		c.Timeout = 20 * time.Millisecond
		return c
	})
	fe.runFn = func(ctx context.Context, _ []string, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	seen := observedKinds(t, r, Spec{Command: "echo", Args: []string{"hi"}})
	if len(seen) != 1 || seen[0] != FailureKindTimeout {
		t.Fatalf("observer saw %v, want exactly [%s]", seen, FailureKindTimeout)
	}
}

func TestFailureObserverSeesEngineErrors(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, nil)
	fe.runFn = func(_ context.Context, _ []string, _ io.Writer) (int, error) {
		return -1, errors.New("engine died")
	}
	seen := observedKinds(t, r, Spec{Command: "echo", Args: []string{"hi"}})
	if len(seen) != 1 || seen[0] != FailureKindExec {
		t.Fatalf("observer saw %v, want exactly [%s]", seen, FailureKindExec)
	}
}

// TestFailureObserverIgnoresARedVerdict is the positive control that keeps this
// metric meaningful.
//
// A non-zero exit is the sandbox WORKING: the command ran and came back red.
// Counting it as a sandbox failure would bury the real failures under every
// ordinary failing build, and the alert built on this counter would be noise
// from the day it shipped.
func TestFailureObserverIgnoresARedVerdict(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, nil)
	fe.runFn = func(_ context.Context, _ []string, _ io.Writer) (int, error) {
		return 1, errors.New("exit status 1")
	}
	seen := observedKinds(t, r, Spec{Command: "echo", Args: []string{"hi"}})
	if len(seen) != 0 {
		t.Fatalf("observer saw %v for a non-zero exit; a red check is a verdict, not a sandbox failure", seen)
	}
}

// TestFailureObserverIgnoresASuccess: a passing command must move nothing.
func TestFailureObserverIgnoresASuccess(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t, nil)
	seen := observedKinds(t, r, Spec{Command: "echo", Args: []string{"hi"}})
	if len(seen) != 0 {
		t.Fatalf("observer saw %v for a passing command", seen)
	}
}

// TestRunnerWithNoObserverDoesNotPanic: the observer is optional wiring; every
// entry point that never sets one still runs commands.
func TestRunnerWithNoObserverDoesNotPanic(t *testing.T) {
	t.Parallel()
	r, fe := newTestRunner(t, nil)
	fe.runFn = func(_ context.Context, _ []string, _ io.Writer) (int, error) {
		return -1, errors.New("engine died")
	}
	if _, err := r.Run(context.Background(), Spec{Command: "echo", Args: []string{"hi"}}); err == nil {
		t.Fatal("expected the engine error")
	}
}

func TestFailureKindsCoverEveryEmittedValue(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		FailureKindRejected:  false,
		FailureKindPreflight: false,
		FailureKindTimeout:   false,
		FailureKindExec:      false,
	}
	for _, k := range FailureKinds() {
		if _, ok := want[k]; !ok {
			t.Errorf("FailureKinds() advertises %q, which no code path emits", k)
		}
		want[k] = true
	}
	for k, listed := range want {
		if !listed {
			t.Errorf("%q is emitted but not advertised by FailureKinds()", k)
		}
	}
}
