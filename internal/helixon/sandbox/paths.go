package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape is returned when a candidate path resolves outside the root
// it was checked against.
var ErrPathEscape = errors.New("sandbox: path escapes its root")

// Contains resolves candidate relative to root and returns the canonical
// absolute path when — and only when — the resolved candidate is root itself
// or lives underneath it.
//
// Why not strings.HasPrefix: the prefix test that this replaces accepts
// "/allowed/../etc/passwd" (never cleaned), accepts "/allowed-other" for the
// root "/allowed" (no separator boundary), and follows a symlink planted
// inside the root straight out of it. All three are real escapes. The check
// here is:
//
//  1. filepath.Clean + make absolute (kills "../" and "./" segments),
//  2. filepath.EvalSymlinks on the longest EXISTING ancestor of the path,
//     with the not-yet-existing tail re-appended (so creating a new file
//     inside the root still works, while a symlinked ancestor is resolved),
//  3. filepath.Rel from the canonical root, rejecting "." -prefixed escapes
//     — which enforces the separator boundary that HasPrefix does not.
//
// A root that does not exist on disk degrades to the lexical form of the same
// check (clean + Rel). That is still non-escapable; it simply cannot resolve
// symlinks that are not there to resolve.
func Contains(root, candidate string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("sandbox: containment root is empty")
	}
	if strings.TrimSpace(candidate) == "" {
		return "", errors.New("sandbox: path is empty")
	}
	if strings.ContainsRune(candidate, 0) {
		return "", fmt.Errorf("%w: path contains a NUL byte", ErrPathEscape)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve root %q: %w", root, err)
	}
	canonRoot := resolveExisting(absRoot)

	abs := candidate
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(canonRoot, abs)
	}
	canonPath := resolveExisting(abs)

	rel, err := filepath.Rel(canonRoot, canonPath)
	if err != nil {
		return "", fmt.Errorf("%w: %q relative to %q: %w", ErrPathEscape, candidate, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q resolves outside %q", ErrPathEscape, candidate, root)
	}
	return canonPath, nil
}

// resolveExisting canonicalizes the longest existing ancestor of p via
// filepath.EvalSymlinks and re-appends the trailing components that do not
// exist yet. A path whose every component is missing comes back cleaned.
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	var tail []string
	cur := p
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved)
		}
		if !errors.Is(err, os.ErrNotExist) && len(tail) > 8 {
			// Give up on pathological inputs rather than walking forever.
			return p
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// CanonicalDir resolves dir to an existing, canonical absolute directory.
// It is the constructor-time counterpart of Contains: bind-mount sources and
// the workspace root must exist before a container can be started, so a
// missing path is an error here rather than a lexical fallback.
func CanonicalDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("sandbox: empty directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve %q: %w", dir, err)
	}
	canon, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve %q: %w", dir, err)
	}
	info, err := os.Stat(canon)
	if err != nil {
		return "", fmt.Errorf("sandbox: stat %q: %w", canon, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox: %q is not a directory", canon)
	}
	return canon, nil
}
