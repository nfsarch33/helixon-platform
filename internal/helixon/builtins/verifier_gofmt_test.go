package builtins

// gofmt_check scope contract (ported from the author contract in the
// ticket): a formatting check must cover only the packages or files the
// caller names, never the whole shared workspace, while the default stays
// the whole tree so nothing weakens.

import (
	"errors"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"reflect"
	"testing"
)

func TestGofmtScopeNoScopeIsWholeTree(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		got, err := GofmtScopeArgs(in)
		if err != nil {
			t.Fatalf("%v: unexpected error %v", in, err)
		}
		if want := []string{"-l", "."}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: got %v want %v", in, got, want)
		}
	}
}

func TestGofmtScopePackagePatternsBecomeCleanDirectories(t *testing.T) {
	got, err := GofmtScopeArgs([]string{"./pkg/...", "./cmd/x", "internal/y/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"-l", "pkg", "cmd/x", "internal/y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGofmtScopeFilesStayFilesAndDuplicatesDrop(t *testing.T) {
	got, err := GofmtScopeArgs([]string{"pkg/a.go", "./pkg/a.go", "pkg/b.go", "pkg/a.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"-l", "pkg/a.go", "pkg/b.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGofmtScopeWholeTreeSubsumesEverything(t *testing.T) {
	for _, in := range [][]string{{"./..."}, {"..."}, {"pkg", "."}, {"./...", "cmd/x"}} {
		got, err := GofmtScopeArgs(in)
		if err != nil {
			t.Fatalf("%v: unexpected error %v", in, err)
		}
		if want := []string{"-l", "."}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: got %v want %v", in, got, want)
		}
	}
}

func TestGofmtScopeRefusals(t *testing.T) {
	for _, in := range [][]string{{""}, {"   "}, {"-w"}, {"pkg", "-d"}, {"/etc"}, {"../x"}, {"pkg/../../x"}, {".."}} {
		if _, err := GofmtScopeArgs(in); !errors.Is(err, ErrBadScope) {
			t.Fatalf("%q: got %v, want ErrBadScope", in, err)
		}
	}
}

func TestGofmtScopeInputSliceIsNotModified(t *testing.T) {
	in := []string{"./pkg/...", "./pkg/..."}
	if _, err := GofmtScopeArgs(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(in, []string{"./pkg/...", "./pkg/..."}) {
		t.Fatalf("GofmtScopeArgs modified its input: %v", in)
	}
}

// The acceptance shape at argv level: a scoped gofmt_check must not name
// anything outside the caller's package, and the unscoped default must
// keep naming the whole tree.
func TestGofmtCheckScopedArgvExcludesTheRestOfTheWorkspace(t *testing.T) {
	checks := map[string]VerifierCheck{}
	for _, c := range DefaultVerifierChecks() {
		checks[c.Name] = c
	}
	scoped, err := buildVerifierArgv(checks["gofmt_check"], []string{"./internal/helixon/builtins/..."}, sandbox.DefaultWorkspaceMount)
	if err != nil {
		t.Fatalf("scoped gofmt_check: %v", err)
	}
	if want := []string{"-l", "internal/helixon/builtins"}; !reflect.DeepEqual(scoped, want) {
		t.Fatalf("scoped argv = %v, want %v", scoped, want)
	}
	whole, err := buildVerifierArgv(checks["gofmt_check"], nil, sandbox.DefaultWorkspaceMount)
	if err != nil {
		t.Fatalf("default gofmt_check: %v", err)
	}
	if want := []string{"-l", "."}; !reflect.DeepEqual(whole, want) {
		t.Fatalf("default argv = %v, want %v (the default must stay the whole tree)", whole, want)
	}
}
