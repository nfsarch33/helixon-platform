// Tests for the TestFileGuardExecutor (v18857, §3.2 Q2a: block writes to
// test files during ticket runs).
//
// The scoring contract says tests are the ruler: an agent being scored on
// a ticket must not be able to touch the ruler. The guard denies
// file_write calls whose target is a Go test file, and ONLY while at
// least one ticket run is in flight — human-driven sessions keep full
// write freedom.
package tooldispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func writeArgs(path string) string {
	return `{"path":"` + path + `","content":"package x"}`
}

func TestTestFileGuardExecutor_DeniesTestFileWritesWhileActive(t *testing.T) {
	inner := &fakeInner{reply: "wrote"}
	guard := &TestFileGuard{}
	exec := NewTestFileGuardExecutor(inner, guard, nil)

	guard.Enter()
	defer guard.Leave()

	_, err := exec.Execute(context.Background(), "file_write", writeArgs("internal/foo/bar_test.go"))
	if !errors.Is(err, ErrTestFileWriteDenied) {
		t.Fatalf("err = %v, want ErrTestFileWriteDenied", err)
	}
	if inner.calls != 0 {
		t.Fatalf("inner.calls = %d, want 0 (deny must not reach the tool)", inner.calls)
	}
}

func TestTestFileGuardExecutor_InactiveAllowsTestFileWrites(t *testing.T) {
	inner := &fakeInner{reply: "wrote"}
	guard := &TestFileGuard{}
	exec := NewTestFileGuardExecutor(inner, guard, nil)

	out, err := exec.Execute(context.Background(), "file_write", writeArgs("bar_test.go"))
	if err != nil {
		t.Fatalf("inactive guard denied: %v", err)
	}
	if out != "wrote" || inner.calls != 1 {
		t.Fatalf("inactive guard must forward: out=%q calls=%d", out, inner.calls)
	}
}

func TestTestFileGuardExecutor_ActiveAllowsNonTestPathsAndReads(t *testing.T) {
	inner := &fakeInner{reply: "ok"}
	guard := &TestFileGuard{}
	exec := NewTestFileGuardExecutor(inner, guard, nil)

	guard.Enter()
	defer guard.Leave()

	cases := []struct{ name, args string }{
		{"file_write", writeArgs("internal/foo/bar.go")},
		{"file_write", writeArgs("testdata/bar_test.go.txt")},
		{"file_read", `{"path":"internal/foo/bar_test.go"}`},
		{"shell", `{"command":"go test ./..."}`},
	}
	for _, tc := range cases {
		if _, err := exec.Execute(context.Background(), tc.name, tc.args); err != nil {
			t.Errorf("%s %s: unexpected denial: %v", tc.name, tc.args, err)
		}
	}
	if inner.calls != len(cases) {
		t.Fatalf("inner.calls = %d, want %d", inner.calls, len(cases))
	}
}

func TestTestFileGuardExecutor_BadArgsStillForward(t *testing.T) {
	inner := &fakeInner{reply: "ok"}
	guard := &TestFileGuard{}
	exec := NewTestFileGuardExecutor(inner, guard, nil)
	guard.Enter()
	defer guard.Leave()

	// Unparseable args are the inner tool's problem, not the guard's: the
	// file_write handler rejects a missing path itself. A guard that
	// errors on malformed JSON would mask the tool's own diagnostics.
	if _, err := exec.Execute(context.Background(), "file_write", "{not-json"); err != nil {
		t.Fatalf("guard must forward malformed args, got %v", err)
	}
}

func TestTestFileGuard_RefCountsConcurrentTickets(t *testing.T) {
	g := &TestFileGuard{}
	if g.Active() {
		t.Fatal("fresh guard must be inactive")
	}
	g.Enter()
	g.Enter()
	if !g.Active() {
		t.Fatal("guard inactive with two tickets in flight")
	}
	g.Leave()
	if !g.Active() {
		t.Fatal("guard dropped while one ticket still in flight")
	}
	g.Leave()
	if g.Active() {
		t.Fatal("guard active after last ticket left")
	}
}

func TestTestFileGuardExecutor_NilGuardForwards(t *testing.T) {
	inner := &fakeInner{reply: "wrote"}
	exec := NewTestFileGuardExecutor(inner, nil, nil)
	if _, err := exec.Execute(context.Background(), "file_write", writeArgs("bar_test.go")); err != nil {
		t.Fatalf("nil guard denied: %v", err)
	}
}

func TestTestFileGuardExecutor_DenialNamesThePath(t *testing.T) {
	guard := &TestFileGuard{}
	exec := NewTestFileGuardExecutor(&fakeInner{}, guard, nil)
	guard.Enter()
	defer guard.Leave()

	_, err := exec.Execute(context.Background(), "file_write", writeArgs("pkg/edge_test.go"))
	if err == nil || !strings.Contains(err.Error(), "pkg/edge_test.go") {
		t.Fatalf("denial should name the denied path, got %v", err)
	}
}
