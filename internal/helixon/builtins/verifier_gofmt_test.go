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
	"strings"
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
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(root, dir, f), []byte("package p\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o750); err != nil {
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
	scoped, err := buildVerifierArgv(checks["gofmt_check"], []string{"./pkg/..."}, root, sandbox.DefaultWorkspaceMount)
	if err != nil {
		t.Fatalf("scoped gofmt_check: %v", err)
	}
	if want := []string{"-l", "--", "pkg"}; !reflect.DeepEqual(scoped, want) {
		t.Fatalf("scoped argv = %v, want %v", scoped, want)
	}
	whole, err := buildVerifierArgv(checks["gofmt_check"], nil, root, sandbox.DefaultWorkspaceMount)
	if err != nil {
		t.Fatalf("default gofmt_check: %v", err)
	}
	if want := []string{"-l", "."}; !reflect.DeepEqual(whole, want) {
		t.Fatalf("default argv = %v, want %v (the default must stay the whole tree)", whole, want)
	}
}

// TestGofmtScopedCheckResolvesAgainstHostWorkspace pins the production
// wiring: a Runner built with a host Workspace and the default mount resolves
// and stats scopes against the HOST directory, while the argv it emits stays
// workspace-relative for the container (which runs with --workdir=WorkspaceMount).
// Before this, the verifier passed WorkspaceMount to the scope resolver and
// every scoped gofmt_check stat'ed /workspace/<scope> on the host and was
// refused — scoped checks were dead in production, and only the tests (fed the
// host dir as the "mount") were green.
func TestGofmtScopedCheckResolvesAgainstHostWorkspace(t *testing.T) {
	root := gofmtWorkspace(t)
	check := checkByName(t, "gofmt_check")

	runner, err := sandbox.NewRunner(sandbox.Config{Enabled: true, Workspace: root})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	cfg := runner.Config()
	if cfg.Workspace == cfg.WorkspaceMount {
		t.Fatalf("test premise: Workspace %q must differ from WorkspaceMount %q", cfg.Workspace, cfg.WorkspaceMount)
	}

	got, err := buildVerifierArgv(check, []string{"./pkg/..."}, cfg.Workspace, cfg.WorkspaceMount)
	if err != nil {
		t.Fatalf("scoped gofmt_check against host workspace: %v", err)
	}
	if want := []string{"-l", "--", "pkg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped argv = %v, want %v", got, want)
	}

	// The bug, pinned: resolving against the mount path refuses every scope.
	if _, err := buildVerifierArgv(check, []string{"./pkg/..."}, cfg.WorkspaceMount, cfg.WorkspaceMount); !errors.Is(err, ErrBadScope) {
		t.Fatalf("resolving scopes against WorkspaceMount: got %v, want ErrBadScope (stat of %s/pkg fails on the host)", err, cfg.WorkspaceMount)
	}

	// The AllowUnsandboxedHostExecution shape: deployments that run tools on
	// the host use the host directory as both the workspace and the mount;
	// scopes must resolve identically there.
	hostShaped, err := sandbox.NewRunner(sandbox.Config{Enabled: true, Workspace: root, WorkspaceMount: root})
	if err != nil {
		t.Fatalf("NewRunner(host-shaped): %v", err)
	}
	hcfg := hostShaped.Config()
	got, err = buildVerifierArgv(check, []string{"./cmd/x"}, hcfg.Workspace, hcfg.WorkspaceMount)
	if err != nil {
		t.Fatalf("scoped gofmt_check, host-shaped config: %v", err)
	}
	if want := []string{"-l", "--", "cmd/x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("host-shaped scoped argv = %v, want %v", got, want)
	}
}

// TestGofmtScopeLiteralFlagDirectoryIsRefused pins M1: a directory literally
// named "-w" that DOES hold a .go file must still be refused by the post-Clean
// flag check. Without that check the stat succeeds (it is a real Go-bearing
// directory) and the argv would carry "-w" into gofmt.
func TestGofmtScopeLiteralFlagDirectoryIsRefused(t *testing.T) {
	root := gofmtWorkspace(t)
	flagDir := filepath.Join(root, "-w")
	if err := os.MkdirAll(flagDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flagDir, "victim.go"), []byte("package w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := GofmtScopeArgs([]string{"./-w"}, root)
	if !errors.Is(err, ErrBadScope) {
		t.Fatalf("got %v, want ErrBadScope", err)
	}
	if err == nil || !strings.Contains(err.Error(), "normalizes to the flag") {
		t.Fatalf("got %v, want the post-Clean refusal naming the flag", err)
	}
}

// TestGofmtScopeSymlinkEscapesAreRefused pins M3: containment must survive
// symlinks. A scope entry that IS a symlink out of the workspace is
// ErrPathEscape; a directory whose only content is a symlink matches no Go
// files because the walk never follows symlinks.
func TestGofmtScopeSymlinkEscapesAreRefused(t *testing.T) {
	root := gofmtWorkspace(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "pkg"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "pkg", "a.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(outside, "pkg"), filepath.Join(root, "escdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "pkg", "a.go"), filepath.Join(root, "escfile.go")); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"escdir", "escfile.go"} {
		_, err := GofmtScopeArgs([]string{scope}, root)
		if !errors.Is(err, ErrBadScope) || !errors.Is(err, sandbox.ErrPathEscape) {
			t.Fatalf("%q: got %v, want ErrBadScope wrapping sandbox.ErrPathEscape", scope, err)
		}
	}

	// A directory whose only content is a symlink is a vacuous scope.
	if err := os.MkdirAll(filepath.Join(root, "onlylink"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "pkg", "a.go"), filepath.Join(root, "onlylink", "in.go")); err != nil {
		t.Fatal(err)
	}
	_, err := GofmtScopeArgs([]string{"onlylink"}, root)
	if !errors.Is(err, ErrBadScope) || !strings.Contains(err.Error(), "matched no Go files") {
		t.Fatalf("onlylink: got %v, want ErrBadScope 'matched no Go files'", err)
	}
}

// TestGofmtScopeDotAndUnderscoreNamesDoNotSatisfyScope: gofmt's own walk
// skips dot- and underscore-prefixed files and directories; a scope whose
// only Go files are such names would pass vacuously if the gate counted them.
func TestGofmtScopeDotAndUnderscoreNamesDoNotSatisfyScope(t *testing.T) {
	root := gofmtWorkspace(t)
	hidden := filepath.Join(root, "hidden")
	if err := os.MkdirAll(hidden, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".x.go", "_y.go"} {
		if err := os.WriteFile(filepath.Join(hidden, name), []byte("package h\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "dotdir", ".sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dotdir", ".sub", "z.go"), []byte("package s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"hidden", "dotdir"} {
		_, err := GofmtScopeArgs([]string{scope}, root)
		if !errors.Is(err, ErrBadScope) || !strings.Contains(err.Error(), "matched no Go files") {
			t.Fatalf("%q: got %v, want ErrBadScope 'matched no Go files' (gofmt skips dot/underscore names)", scope, err)
		}
	}
}
