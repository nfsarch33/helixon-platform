package builtins

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrBadScope marks a gofmt_check scope argument that was refused rather
// than normalized: a blank entry, a flag, an absolute path, or anything
// that cleans to outside the workspace. Refusing beats sanitizing -- a
// caller that says "-w" or "/etc" is not saying anything a formatter
// should act on.
var ErrBadScope = errors.New("gofmt_check: bad scope argument")

// GofmtScopeArgs turns a gofmt_check caller's scope arguments into the
// full `gofmt` argv. No scope keeps today's whole-tree default (["-l",
// "."]), so the gate never weakens by omission; a named scope covers only
// what the caller named, so one student's unformatted sibling package can
// no longer fail every other student's ticket.
//
// Normalization rules (mirrored by the contract tests):
//   - "./pkg/..." and "./pkg" both mean the directory "pkg"; a file stays
//     a file; results are path.Clean'd;
//   - ".", "./..." or "..." anywhere means the whole tree;
//   - duplicates (after cleaning) drop, first occurrence wins, order kept;
//   - blank entries, flags, absolute paths and anything that cleans to
//     ".." or under it are ErrBadScope. The input slice is not modified.
func GofmtScopeArgs(scope []string) ([]string, error) {
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
		if cleaned == ".." || cleaned == "../" || strings.HasPrefix(cleaned, "../") {
			return nil, fmt.Errorf("%w: %q escapes the workspace", ErrBadScope, entry)
		}
		if cleaned == "." || cleaned == "..." {
			// The whole tree subsumes every other entry.
			return []string{"-l", "."}, nil
		}
		cleaned = strings.TrimSuffix(cleaned, "/...")
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return append([]string{"-l"}, out...), nil
}
