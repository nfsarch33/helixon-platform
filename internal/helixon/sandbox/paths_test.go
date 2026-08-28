package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContains_TableDriven covers the escapes that the strings.HasPrefix
// check this replaces let through, plus the ordinary allow cases.
func TestContains_TableDriven(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// t.TempDir on macOS/Linux can itself sit behind a symlink (/tmp ->
	// /private/tmp); canonicalize so the expectations are stable.
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", root, err)
	}
	if err := os.MkdirAll(filepath.Join(canonRoot, "nested", "deep"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(canonRoot, "inside.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	tests := []struct {
		name      string
		candidate string
		wantErr   bool
		wantIs    error
	}{
		{name: "root itself", candidate: canonRoot},
		{name: "existing file inside", candidate: filepath.Join(canonRoot, "inside.txt")},
		{name: "nested existing dir", candidate: filepath.Join(canonRoot, "nested", "deep")},
		{name: "not-yet-existing file inside (a write target)", candidate: filepath.Join(canonRoot, "nested", "new.txt")},
		{name: "relative path resolves under root", candidate: "nested/new.txt"},

		{name: "dot-dot traversal", candidate: filepath.Join(canonRoot, "..", "escaped.txt"), wantErr: true, wantIs: ErrPathEscape},
		{name: "relative dot-dot traversal", candidate: "../../etc/passwd", wantErr: true, wantIs: ErrPathEscape},
		{name: "deep dot-dot traversal back out", candidate: filepath.Join(canonRoot, "nested", "..", "..", "etc"), wantErr: true, wantIs: ErrPathEscape},
		{name: "absolute path elsewhere", candidate: "/etc/passwd", wantErr: true, wantIs: ErrPathEscape},
		{name: "sibling sharing the root's prefix", candidate: canonRoot + "-evil/secrets", wantErr: true, wantIs: ErrPathEscape},
		{name: "NUL byte", candidate: filepath.Join(canonRoot, "a\x00b"), wantErr: true, wantIs: ErrPathEscape},
		{name: "empty", candidate: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Contains(canonRoot, tt.candidate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Contains(%q, %q) error = %v, wantErr %v", canonRoot, tt.candidate, err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
					t.Fatalf("error %v is not %v", err, tt.wantIs)
				}
				return
			}
			if !strings.HasPrefix(got, canonRoot) {
				t.Fatalf("resolved %q is not under %q", got, canonRoot)
			}
		})
	}
}

// TestContains_SymlinkEscapeRejected is the case a prefix check cannot see:
// a symlink that lives inside the root and points out of it. The candidate
// string is legitimately under the root; only resolution reveals the escape.
func TestContains_SymlinkEscapeRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(canonRoot, "innocent")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Lexically this is "inside the root". It is not.
	candidate := filepath.Join(link, "secret.txt")
	if !strings.HasPrefix(candidate, canonRoot) {
		t.Fatalf("precondition: %q should lexically look contained", candidate)
	}
	if _, err := Contains(canonRoot, candidate); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("symlink escape must be rejected, got err=%v", err)
	}
}

// TestContains_EmptyRootRejected proves the guard fails closed rather than
// treating "no root" as "any path is fine".
func TestContains_EmptyRootRejected(t *testing.T) {
	t.Parallel()
	if _, err := Contains("", "/etc/passwd"); err == nil {
		t.Fatal("an empty containment root must be an error, not a wildcard")
	}
}

// TestContains_NonExistentRootStillContains proves the lexical fallback keeps
// containment when the root does not exist on disk (the in-container
// workspace mount, for example, never exists on the host).
func TestContains_NonExistentRootStillContains(t *testing.T) {
	t.Parallel()
	root := "/workspace-does-not-exist-18779"
	if _, err := Contains(root, root+"/pkg/file.go"); err != nil {
		t.Fatalf("path under a non-existent root should be contained: %v", err)
	}
	if _, err := Contains(root, "/etc/passwd"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("escape under a non-existent root must still be rejected, got %v", err)
	}
	if _, err := Contains(root, root+"/../etc/passwd"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("traversal under a non-existent root must still be rejected, got %v", err)
	}
}

func TestCanonicalDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := CanonicalDir(dir)
	if err != nil {
		t.Fatalf("CanonicalDir: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected absolute path, got %q", got)
	}
	if _, err := CanonicalDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing directory must be an error at construction time")
	}
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := CanonicalDir(file); err == nil {
		t.Fatal("a file is not a directory and must be rejected")
	}
	if _, err := CanonicalDir("  "); err == nil {
		t.Fatal("blank directory must be rejected")
	}
}
