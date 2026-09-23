package builtins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
)

// ErrBadScope marks a gofmt_check scope argument that was refused rather
// than normalized: a blank entry, a flag (raw or produced by cleaning),
// an absolute path, anything that cleans to outside the workspace, or a
// scope that matches no Go files. Refusing beats sanitizing — a caller
// that says "-w" or "/etc" is not saying anything a formatter should
// act on.
var ErrBadScope = errors.New("gofmt_check: bad scope argument")

// GofmtScopeArgs turns a gofmt_check caller's scope arguments into the
// full `gofmt` argv, resolved against workspaceRoot. No scope keeps the
// whole-tree default (["-l", "."]), so the gate never weakens by
// omission; a named scope covers only what the caller named, so one
// student's unformatted sibling package can no longer fail every other
// student's ticket.
//
// Normalization rules (pinned by verifier_gofmt_test.go, adapted from the
// author contract with the review's hardening):
//   - "./pkg/..." and "./pkg" both mean the directory "pkg"; a file stays
//     a file; results are path.Clean'd; duplicates drop, first wins,
//     order kept. A bare directory is RECURSIVE (gofmt semantics; unlike
//     go_test, which errors on an empty package match).
//   - ".", "./..." or "..." anywhere means the whole tree.
//   - blank entries, absolute paths and anything that cleans to ".." are
//     ErrBadScope.
//   - a flag is refused TWICE: on the raw entry and on the cleaned result
//     ("./-w" cleans to "-w"), because gofmt would otherwise execute it —
//     `-w` rewrites files, `-cpuprofile` overwrites one — through an
//     allow-listed check.
//   - scoped argv carries "--" so a path that looks like a flag can never
//     become one even if a future edit regresses the refusal; the
//     unscoped default keeps today's exact argv.
//   - every scope must resolve inside workspaceRoot (symlink-aware, via
//     sandbox.Contains) and match at least one .go file — file or
//     directory contents — so an empty or docs-only scope cannot buy a
//     vacuous pass.
//
// The input slice is not modified.
func GofmtScopeArgs(scope []string, workspaceRoot string) ([]string, error) {
	if len(scope) == 0 {
		return []string{"-l", "."}, nil
	}
	out := make([]string, 0, len(scope))
	seen := make(map[string]bool, len(scope))
	for _, raw := range scope {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, fmt.Errorf("%w: blank entry in %q", ErrBadScope, raw)
		}
		if strings.HasPrefix(entry, "-") {
			return nil, fmt.Errorf("%w: %q is a flag, not a scope", ErrBadScope, entry)
		}
		if path.IsAbs(entry) {
			return nil, fmt.Errorf("%w: %q is absolute; scopes are workspace-relative", ErrBadScope, entry)
		}
		cleaned := path.Clean(entry)
		if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, fmt.Errorf("%w: %q escapes the workspace", ErrBadScope, entry)
		}
		if cleaned == "." || cleaned == "..." {
			// The whole tree subsumes every other entry.
			return []string{"-l", "."}, nil
		}
		cleaned = strings.TrimSuffix(cleaned, "/...")
		// The second flag check: "./-w" and "pkg/../-w" clean to "-w".
		// Checking the raw entry alone lets exactly these through.
		if strings.HasPrefix(cleaned, "-") {
			return nil, fmt.Errorf("%w: %q normalizes to the flag %q", ErrBadScope, entry, cleaned)
		}
		if seen[cleaned] {
			continue
		}
		if workspaceRoot != "" {
			canon, err := sandbox.Contains(workspaceRoot, cleaned)
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %v", ErrBadScope, entry, err)
			}
			if err := scopeMatchesGoFiles(filepath.FromSlash(canon), cleaned); err != nil {
				return nil, err
			}
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return append([]string{"-l", "--"}, out...), nil
}

// scopeMatchesGoFiles refuses scopes that cannot run the gate: a file
// scope must be an existing .go file; a directory scope must hold at
// least one .go file anywhere under it. Symlinks are not followed.
func scopeMatchesGoFiles(absPath, display string) error {
	fi, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", ErrBadScope, display, err)
	}
	if !fi.IsDir() {
		if !strings.HasSuffix(absPath, ".go") {
			return fmt.Errorf("%w: %q is not a Go file", ErrBadScope, display)
		}
		return nil
	}
	found := false
	//nolint:gosec // G304: absPath is sandbox-contained above
	err = filepath.WalkDir(absPath, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".go") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %q: %v", ErrBadScope, display, err)
	}
	if !found {
		return fmt.Errorf("%w: %q matched no Go files", ErrBadScope, display)
	}
	return nil
}
