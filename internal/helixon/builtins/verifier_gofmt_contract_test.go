package builtins_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon/builtins"
)

// The author contract for gofmt_check scoping (copilot workspace batch 5,
// gofmtscope) is reproduced verbatim below, bound to the shipped
// implementation through aliases: the rules stay the proof, and a future
// edit that quietly changes normalization semantics fails here first.

var Args = builtins.GofmtScopeArgs

var ErrBadScope = builtins.ErrBadScope

// Contract: turn a verifier's scope arguments into the argv for `gofmt -l`, so a student's formatting
// check covers only the packages or files it owns instead of the whole shared workspace.
// Harvest target: the fleet verifier's gofmt_check scope fix.
//
// Rules the tests pin:
//   - no scope → ["-l", "."] (whole tree, today's behaviour);
//   - "./pkg/..." and "./pkg" → "pkg"; "pkg/a.go" stays a file; results are path.Clean'd;
//   - "." or "./..." or "..." anywhere means the whole tree → ["-l", "."];
//   - duplicates (after cleaning) are dropped, first occurrence wins, order otherwise kept;
//   - an empty/blank entry, a flag ("-w"), an absolute path, or anything that cleans to ".." or
//     "../…" is ErrBadScope (no flag injection, no escaping the workspace).

func TestNoScopeIsWholeTree(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		got, err := Args(in)
		if err != nil {
			t.Fatalf("%v: unexpected error %v", in, err)
		}
		if want := []string{"-l", "."}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: got %v want %v", in, got, want)
		}
	}
}

func TestPackagePatternsBecomeCleanDirectories(t *testing.T) {
	got, err := Args([]string{"./pkg/...", "./cmd/x", "internal/y/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"-l", "pkg", "cmd/x", "internal/y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFilesStayFilesAndDuplicatesDrop(t *testing.T) {
	got, err := Args([]string{"pkg/a.go", "./pkg/a.go", "pkg/b.go", "pkg/a.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"-l", "pkg/a.go", "pkg/b.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestWholeTreeSubsumesEverything(t *testing.T) {
	for _, in := range [][]string{{"./..."}, {"..."}, {"pkg", "."}, {"./...", "cmd/x"}} {
		got, err := Args(in)
		if err != nil {
			t.Fatalf("%v: unexpected error %v", in, err)
		}
		if want := []string{"-l", "."}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: got %v want %v", in, got, want)
		}
	}
}

func TestRefusals(t *testing.T) {
	for _, in := range [][]string{{""}, {"   "}, {"-w"}, {"pkg", "-d"}, {"/etc"}, {"../x"}, {"pkg/../../x"}, {".."}} {
		if _, err := Args(in); !errors.Is(err, ErrBadScope) {
			t.Fatalf("%q: got %v, want ErrBadScope", in, err)
		}
	}
}

func TestInputSliceIsNotModified(t *testing.T) {
	in := []string{"./pkg/...", "./pkg/..."}
	if _, err := Args(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(in, []string{"./pkg/...", "./pkg/..."}) {
		t.Fatalf("Args modified its input: %v", in)
	}
}
