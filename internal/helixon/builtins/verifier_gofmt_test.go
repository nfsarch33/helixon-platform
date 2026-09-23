package builtins

// gofmt_check scoping: a formatting check must cover only the packages or
// files the caller names, never the whole shared workspace, while the
// default stays the whole tree so nothing weakens. The rule set started
// from the author contract in the ticket and was hardened in review:
// flags are refused twice (raw and post-clean) and scoped argv carries
// "--", because a normalization that can emit "-w" hands a reformatting
// tool to an allow-listed check; and every scope must match a Go file,
// because an empty directory buys a vacuous pass.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
)

// gofmtWorkspace builds a scratch workspace:
//
//	pkg/a.go, pkg/b.go, cmd/x/main.go, docs/readme.md, empty/
func gofmtWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for dir, files := range map[string][]string{
		"pkg":       {"a.go", "b.go"},
		"cmd/x":     {"main.go"},
		"docs":      {"readme.md"},
		"deep/dgrp": {"n.go"},
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(root, dir, f), []byte("package p\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGofmtScopeNoScopeIsWholeTree(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		got, err := GofmtScopeArgs(in, "")
		if err != nil {
			t.Fatalf("%v: unexpected error %v", in, err)
		}
		if want := []string{"-l", "."}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: got %v want %v", in, got, want)
		}
	}
}

func TestGofmtScopePackagePatternsBecomeCleanDirectories(t *testing.T) {
	root := gofmtWorkspace(t)
	got, err := GofmtScopeArgs([]string{"./pkg/...", "./cmd/x", "deep/dgrp/"}, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"-l", "--", "pkg", "cmd/x", "deep/dgrp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGofmtScopeFilesStayFilesAndDuplicatesDrop(t *testing.T) {
	root := gofmtWorkspace(t)
	got, err := GofmtScopeArgs([]string{"pkg/a.go", "./pkg/a.go", "pkg/b.go", "pkg/a.go"}, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"-l", "--", "pkg/a.go", "pkg/b.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGofmtScopeWholeTreeSubsumesEverything(t *testing.T) {
	root := gofmtWorkspace(t)
	for _, in := range [][]string{{"./..."}, {"..."}, {"pkg", "."}, {"./...", "cmd/x"}} {
		got, err := GofmtScopeArgs(in, root)
		if err != nil {
			t.Fatalf("%v: unexpected error %v", in, err)
		}
		if want := []string{"-l", "."}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: got %v want %v", in, got, want)
		}
	}
}

// TestGofmtScopeFlagInjectionIsRefused is the security pin: "./-w",
// "pkg/../-w" and friends clean to a flag after the raw-entry check has
// passed, and gofmt would execute it — `-w` rewrites files through an
// allow-listed check.
func TestGofmtScopeFlagInjectionIsRefused(t *testing.T) {
	root := gofmtWorkspace(t)
	for _, in := range [][]string{
		{""}, {"   "}, {"-w"}, {"pkg", "-d"},
		{"/etc"}, {"../x"}, {"pkg/../../x"}, {".."},
		{"./-w"}, {"pkg/../-w"}, {" ./-w"},
		{"./-cpuprofile=other/victim.go", "pkg"},
		{"./-r=a->b", "./-w", "pkg"},
	} {
		if _, err := GofmtScopeArgs(in, root); !errors.Is(err, ErrBadScope) {
			t.Fatalf("%q: got %v, want ErrBadScope", in, err)
		}
	}
}

// TestGofmtScopeVacuousScopesAreRefused: a scope that matches no Go files
// (an empty directory, a docs-only directory) would run gofmt over
// nothing and pass — the formatting-gate version of an empty test suite.
func TestGofmtScopeVacuousScopesAreRefused(t *testing.T) {
	root := gofmtWorkspace(t)
	for _, in := range [][]string{
		{"empty"},
		{"docs"},
		{"pkg/missing.go"},
		{"docs/readme.md"},
	} {
		if _, err := GofmtScopeArgs(in, root); !errors.Is(err, ErrBadScope) {
			t.Fatalf("%q: got %v, want ErrBadScope (no Go files in scope)", in, err)
		}
	}
}

func TestGofmtScopeInputSliceIsNotModified(t *testing.T) {
	root := gofmtWorkspace(t)
	in := []string{"./pkg/...", "./pkg/..."}
	if _, err := GofmtScopeArgs(in, root); err != nil {
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
	root := gofmtWorkspace(t)
	checks := map[string]VerifierCheck{}
	for _, c := range DefaultVerifierChecks() {
		checks[c.Name] = c
	}
	scoped, err := buildVerifierArgv(checks["gofmt_check"], []string{"./pkg/..."}, root)
	if err != nil {
		t.Fatalf("scoped gofmt_check: %v", err)
	}
	if want := []string{"-l", "--", "pkg"}; !reflect.DeepEqual(scoped, want) {
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
