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

// TestFailureObserverSeesPreflightRefusals: everything that stops a command
// before the container starts is one kind, because it is one operator action —
// look at the host and the config.
func TestFailureObserverSeesPreflightRefusals(t *testing.T) {
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
			name: "command outside the allow-list",
			setup: func(t *testing.T) (*Runner, Spec) {
				r, _ := newTestRunner(t, func(c Config) Config {
					c.AllowedCommands = []string{"go"}
					return c
				})
				return r, Spec{Command: "curl", Args: []string{"example.com"}}
			},
		},
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
	want := map[string]bool{FailureKindPreflight: false, FailureKindTimeout: false, FailureKindExec: false}
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
