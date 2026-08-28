package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestContainArgv_RefusesEscapes is the negative half of the v18783 cwd jail.
// Every case here is an argument vector that, on the host execution path,
// addressed something outside the workspace.
func TestContainArgv_RefusesEscapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// A symlink planted inside the workspace pointing out of it. This is the
	// case a strings.HasPrefix check passes and Contains catches.
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tests := []struct {
		name string
		argv []string
	}{
		{"absolute path outside", []string{"/etc/passwd"}},
		{"parent traversal", []string{".."}},
		{"traversal segment", []string{"../../etc/shadow"}},
		{"traversal hidden mid-path", []string{"sub/../../../etc/hosts"}},
		{"symlink out of the workspace", []string{link}},
		{"escape among ordinary arguments", []string{"-l", "inside.txt", "/var/log"}},
		// Found by mutation testing: skipping every "-"-prefixed entry left
		// `sort --files0-from=/etc/passwd` — an allow-listed command reading
		// an arbitrary host file — outside the jail. deniedFlags does not
		// cover it because it grants neither execution nor a write.
		{"absolute path inside a long flag value", []string{"--files0-from=/etc/passwd"}},
		{"traversal inside a long flag value", []string{"--exclude-from=./../../etc/hosts"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ContainArgv(root, tt.argv)
			if err == nil {
				t.Fatalf("ContainArgv(%q) = nil; the argument addresses something outside the workspace", tt.argv)
			}
			if !errors.Is(err, ErrPathEscape) {
				t.Errorf("error = %v, want it to wrap ErrPathEscape", err)
			}
		})
	}
}

// TestContainArgv_AllowsOrdinaryWork is the POSITIVE CONTROL.
//
// A jail that refuses everything passes every escape test above and makes the
// shell tool useless. These are the argument vectors an agent legitimately
// sends, and this test fails if the containment rule is tightened into a
// blanket refusal — for instance by treating flags or bare patterns as paths.
func TestContainArgv_AllowsOrdinaryWork(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tests := []struct {
		name string
		argv []string
	}{
		{"no arguments", nil},
		{"bare filename", []string{"main.go"}},
		{"the workspace itself", []string{"."}},
		{"explicit relative", []string{"./sub"}},
		{"nested relative", []string{"sub/main.go"}},
		{"a path that does not exist yet", []string{"sub/generated.txt"}},
		{"absolute path inside", []string{filepath.Join(root, "sub")}},
		{"short flags", []string{"-la", "sub"}},
		{"long flag with no value", []string{"--color", "sub"}},
		{"long flag whose value is not a path", []string{"--color=auto", "sub"}},
		{"long flag with a slash in its value", []string{"--include=*/testdata/*", "sub"}},
		{"long flag whose value stays inside", []string{"--exclude-from=sub/skip.txt", "sub"}},
		{"a grep pattern that is not a path", []string{"-q", "func main", "main.go"}},
		{"traversal that stays inside", []string{"sub/../main.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := ContainArgv(root, tt.argv); err != nil {
				t.Fatalf("ContainArgv(%q) = %v; this is ordinary in-workspace work and must be allowed", tt.argv, err)
			}
		})
	}
}
