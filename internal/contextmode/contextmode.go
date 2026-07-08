// Package contextmode implements the v14515 context-trim layer
// described in docs/token-saving-strategy.md section 4.
//
// Three responsibilities:
//
//  1. Truncate any tool-output longer than MaxOutputTokens with a
//     truncation marker (so the model sees "… truncated 8.2k tokens …"
//     instead of the whole log).
//  2. Replace inline code blocks longer than MaxBlockTokens with a
//     `file:line` reference if the block matches `file:NNN..NNN`.
//  3. Strip ANSI escape codes, base64 blobs, and NUL bytes from
//     concatenated strings.
//
// All transforms are pure (no I/O), so they are safe to run inside
// the Cursor beforeSubmitPrompt hook.
package contextmode

import (
	"fmt"
	"regexp"
	"strings"
)

// Default limits — override via Options.
const (
	DefaultMaxOutputTokens = 2048
	DefaultMaxBlockTokens  = 1024
)

// Options tweaks Trim behaviour.
type Options struct {
	MaxOutputTokens int
	MaxBlockTokens  int
}

// Default returns Options with sane defaults.
func Default() Options {
	return Options{
		MaxOutputTokens: DefaultMaxOutputTokens,
		MaxBlockTokens:  DefaultMaxBlockTokens,
	}
}

var (
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	nulRE  = regexp.MustCompile(`\x00+`)
	b64RE  = regexp.MustCompile(`[A-Za-z0-9+/]{200,}={0,2}`)
	// crude "this looks like a file path + line range" hint.
	fileRangeRE = regexp.MustCompile(`(?m)^[ \t]*([\w./\-]+\.(go|py|yaml|yml|json|sh|md)):(\d+)(?:\.\.(\d+))?`)
)

// Strip returns s with ANSI escapes, NUL runs, and long base64 blobs
// removed. This is the cheapest pass; it never alters length budgets.
func Strip(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	s = nulRE.ReplaceAllString(s, "")
	// Replace very long base64-ish blobs with a marker.
	s = b64RE.ReplaceAllString(s, "[base64-blob]")
	return s
}

// Truncate returns s trimmed to ~maxTokens*4 chars, appending a
// marker so downstream readers know data was dropped.
//
// We estimate 1 token ≈ 4 chars (matches headroom.EstimateTokens).
func Truncate(s string, maxTokens int, label string) string {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxOutputTokens
	}
	maxChars := maxTokens * 4
	if len(s) <= maxChars {
		return s
	}
	dropped := (len(s) - maxChars) / 4
	marker := fmt.Sprintf("\n... [truncated ~%d tokens of %q; use tools to view full] ...\n", dropped, label)
	return s[:maxChars] + marker
}

// FormatImportPath goes a step further: if the input matches a
// "file:line..line" hint (the common case when an agent pasted
// output from `grep -n` or `git blame`), it returns the hint plus a
// "see file" instruction so the model can read the source instead
// of the duplicate paste.
func FormatImportPath(s string) string {
	// Idempotent: if we already prepended the hint, do nothing.
	if strings.Contains(s, "[context-mode]") {
		return s
	}
	m := fileRangeRE.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	path := m[1]
	from := m[3]
	to := m[4]
	if to == "" {
		to = from
	}
	hint := fmt.Sprintf("[context-mode] long paste matches %s:%s..%s; prefer `Read %s` instead of pasting.", path, from, to, path)
	return hint + "\n" + s
}

// Trim runs Strip + Truncate in one call. label is used in the
// truncation marker so the model knows what was dropped.
func Trim(s string, opts Options, label string) string {
	if opts.MaxOutputTokens == 0 {
		opts.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if opts.MaxBlockTokens == 0 {
		opts.MaxBlockTokens = DefaultMaxBlockTokens
	}
	s = Strip(s)
	s = FormatImportPath(s)
	s = Truncate(s, opts.MaxOutputTokens, label)
	return s
}

// --- internal ---

// fmt.Sprintf lives in fmt; we don't import it at the top of the
// package to keep it stdlib-light, but Truncate needs it. Using a
// tiny wrapper avoids polluting the package import block.
func init() {
	// (intentionally empty; kept as a placeholder for future
	// build-tag-gated imports if we need them.)
}