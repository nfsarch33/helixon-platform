// Package sandbox puts a real boundary around agent tool execution.
//
// The model is taken from the openclaw gateway's per-agent sandbox: a
// container with hardened defaults (no network, read-only root filesystem,
// every capability dropped, no-new-privileges, a non-root user, explicit
// memory and pid ceilings, a tmpfs for scratch), a canonicalized bind
// allow-list, and an explicit workspace mount with none/ro/rw access. On top
// of that it takes Anthropic's sandbox-runtime rule that filesystem and
// network isolation are BOTH required: network isolation without filesystem
// isolation permits escape, filesystem isolation without network isolation
// permits exfiltration — so a writable workspace with a reachable network is
// rejected at construction rather than merely discouraged.
//
// Two properties are load-bearing and are asserted by mutation-tested cases
// in this package:
//
//   - The sandbox NEVER silently degrades to host execution. A missing image
//     or a missing engine is a loud, actionable error. A sandbox that quietly
//     falls back to running on the host is worse than no sandbox, because the
//     operator believes a boundary exists that does not.
//   - Command output is ALWAYS bounded, and the bound never reaches the child
//     as EPIPE (see BoundedBuffer).
//
// Podman is the only supported engine.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults for a hardened sandbox. The image is pinned deliberately: an
// unpinned "latest" turns a supply-chain change into a silent behavior
// change inside the one component whose job is containment.
const (
	// DefaultEngine is the only supported container engine.
	DefaultEngine = "podman"
	// DefaultImage is the pinned default execution image. It carries the Go
	// toolchain because the verifier checks (go build/test/vet) run in it.
	DefaultImage = "docker.io/library/golang:1.26-bookworm"
	// DefaultNetwork disables networking entirely.
	DefaultNetwork = "none"
	// DefaultUser is nobody:nogroup — non-root inside the container.
	DefaultUser = "65534:65534"
	// DefaultMemoryLimit caps container memory.
	DefaultMemoryLimit = "512m"
	// DefaultPidsLimit caps the process count (fork-bomb ceiling).
	DefaultPidsLimit = 256
	// DefaultTmpfsSize bounds the scratch tmpfs mounted at /tmp.
	DefaultTmpfsSize = "64m"
	// DefaultWorkspaceMount is where the workspace appears inside the container.
	DefaultWorkspaceMount = "/workspace"
	// DefaultTimeout is the hard wall-clock ceiling for one sandboxed command.
	DefaultTimeout = 120 * time.Second
	// DefaultMaxOutputBytes is the retention cap for combined output.
	DefaultMaxOutputBytes = 64 * 1024
	// MaxArgs bounds the argument vector a caller may submit.
	MaxArgs = 128
	// MaxArgLen bounds a single argument.
	MaxArgLen = 4096
)

// WorkspaceAccess describes how the workspace directory is mounted.
type WorkspaceAccess string

// Workspace access modes.
const (
	WorkspaceNone WorkspaceAccess = "none"
	WorkspaceRO   WorkspaceAccess = "ro"
	WorkspaceRW   WorkspaceAccess = "rw"
)

// DefaultAllowedCommands is the hardened shell allow-list.
//
// git, go and make are deliberately absent. Each is a general-purpose code
// execution primitive: `git -c core.pager=<cmd> log`, `go run`/`go generate`,
// and a make target all execute arbitrary code, so allow-listing them by name
// allow-lists everything. They are reachable through the sandboxed verifier
// (go build/test/vet) and through a sandboxed shell, where escaping the
// allow-list only gets you a network-less, read-only, capability-less
// container instead of the fleet host.
var DefaultAllowedCommands = []string{
	"cat", "date", "echo", "find", "grep", "head", "hostname",
	"ls", "pwd", "sort", "tail", "test", "uname", "wc", "whoami",
}

// Bind is one explicit bind mount. Host is canonicalized at construction.
type Bind struct {
	Host      string
	Container string
	ReadWrite bool
}

// Config describes one agent's sandbox.
type Config struct {
	// Enabled turns the sandbox on. It defaults to true; see Normalize.
	Enabled bool
	// Engine must resolve to podman.
	Engine string
	// Image is the pinned execution image.
	Image string
	// Network is "none" (default) or "bridge".
	Network string
	// User is the uid:gid the container process runs as. Must not be root.
	User string
	// MemoryLimit / PidsLimit / TmpfsSize are podman resource limits.
	MemoryLimit string
	PidsLimit   int
	TmpfsSize   string
	// Workspace is the host directory the agent works in.
	Workspace string
	// WorkspaceAccess is none, ro, or rw.
	WorkspaceAccess WorkspaceAccess
	// WorkspaceMount is the in-container path for Workspace.
	WorkspaceMount string
	// Binds are additional explicit mounts. Every source is canonicalized
	// and must exist.
	Binds []Bind
	// Env is the ONLY environment the container receives. The host
	// environment is never forwarded: a fleet host's environment holds
	// router bearers and 1Password service tokens.
	Env map[string]string
	// AllowedCommands is the shell allow-list enforced before a command is
	// handed to the engine.
	AllowedCommands []string
	// Timeout is the hard ceiling for one command.
	Timeout time.Duration
	// MaxOutputBytes is the combined-output retention cap.
	MaxOutputBytes int
	// AllowUnsandboxedHostExecution is the explicit, default-off escape
	// hatch. Setting it true runs agent tool commands directly on the host
	// with no container boundary at all: no network isolation, no filesystem
	// isolation, the agent's full ambient authority. It exists so that an
	// operator who needs host execution has to say so in writing, in the
	// config, under a name that cannot be misread as harmless.
	AllowUnsandboxedHostExecution bool
	// DenyUnlistedTools flips the tool policy default from path-guarded to
	// denied, so a tool nobody has classified cannot execute at all.
	DenyUnlistedTools bool
}

// Normalize fills defaults and returns a config ready for validation.
//
// Config is passed and returned BY VALUE throughout this package on purpose:
// Normalize and Validate hand back a copy, so a caller cannot end up holding
// a pointer to a config that something else later mutated out from under the
// running sandbox. That is worth more than the copy.
//
//nolint:gocritic // hugeParam: value semantics are the point; see above
func (c Config) Normalize(workingDir string) Config {
	if c.Engine == "" {
		c.Engine = DefaultEngine
	}
	if c.Image == "" {
		c.Image = DefaultImage
	}
	if c.Network == "" {
		c.Network = DefaultNetwork
	}
	if c.User == "" {
		c.User = DefaultUser
	}
	if c.MemoryLimit == "" {
		c.MemoryLimit = DefaultMemoryLimit
	}
	if c.PidsLimit <= 0 {
		c.PidsLimit = DefaultPidsLimit
	}
	if c.TmpfsSize == "" {
		c.TmpfsSize = DefaultTmpfsSize
	}
	if c.WorkspaceMount == "" {
		c.WorkspaceMount = DefaultWorkspaceMount
	}
	if c.WorkspaceAccess == "" {
		c.WorkspaceAccess = WorkspaceRW
	}
	if c.Workspace == "" {
		c.Workspace = workingDir
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if len(c.AllowedCommands) == 0 {
		c.AllowedCommands = append([]string(nil), DefaultAllowedCommands...)
	}
	return c
}

var rootUserRE = regexp.MustCompile(`^(0|root)(:|$)`)

// Validate checks the invariants that make the config a boundary rather than
// a suggestion. It canonicalizes Workspace and every bind source on the
// returned copy.
//
//nolint:gocritic // hugeParam: value semantics are deliberate, see Normalize
func (c Config) Validate() (Config, error) {
	if filepath.Base(c.Engine) != DefaultEngine {
		return c, fmt.Errorf("sandbox: engine %q is not supported; podman is the only permitted engine", c.Engine)
	}
	if strings.TrimSpace(c.Image) == "" {
		return c, errors.New("sandbox: image is required")
	}
	switch c.Network {
	case "none", "bridge":
	default:
		return c, fmt.Errorf("sandbox: network %q is not supported (use \"none\" or \"bridge\")", c.Network)
	}
	if rootUserRE.MatchString(strings.TrimSpace(c.User)) {
		return c, fmt.Errorf("sandbox: user %q is root; the sandbox requires a non-root user", c.User)
	}
	switch c.WorkspaceAccess {
	case WorkspaceNone, WorkspaceRO, WorkspaceRW:
	default:
		return c, fmt.Errorf("sandbox: workspace access %q is not supported (use none, ro, or rw)", c.WorkspaceAccess)
	}
	// Anthropic sandbox-runtime rule: filesystem and network isolation are
	// both required. A writable workspace plus a reachable network is an
	// exfiltration path, so refuse the combination outright.
	if c.Network != "none" && c.WorkspaceAccess == WorkspaceRW {
		return c, errors.New("sandbox: network is enabled with a writable workspace; that combination is an exfiltration path — set network: none or workspace access: ro")
	}
	if !filepath.IsAbs(c.WorkspaceMount) || filepath.Clean(c.WorkspaceMount) == "/" {
		return c, fmt.Errorf("sandbox: workspace_mount %q must be an absolute non-root path", c.WorkspaceMount)
	}
	if c.Workspace == "" {
		return c, errors.New("sandbox: workspace is required when the sandbox is enabled")
	}
	canonWorkspace, err := CanonicalDir(c.Workspace)
	if err != nil {
		return c, fmt.Errorf("sandbox: workspace: %w", err)
	}
	c.Workspace = canonWorkspace

	binds := make([]Bind, 0, len(c.Binds))
	for _, b := range c.Binds {
		canon, bErr := CanonicalDir(b.Host)
		if bErr != nil {
			return c, fmt.Errorf("sandbox: bind %q: %w", b.Host, bErr)
		}
		if canon == string(filepath.Separator) {
			return c, errors.New("sandbox: refusing to bind-mount the host root")
		}
		if !filepath.IsAbs(b.Container) || filepath.Clean(b.Container) == "/" {
			return c, fmt.Errorf("sandbox: bind target %q must be an absolute non-root path", b.Container)
		}
		b.Host = canon
		b.Container = filepath.Clean(b.Container)
		binds = append(binds, b)
	}
	c.Binds = binds

	if c.Timeout <= 0 {
		return c, errors.New("sandbox: timeout must be positive")
	}
	if c.MaxOutputBytes <= 0 {
		return c, errors.New("sandbox: max_output_bytes must be positive")
	}
	return c, nil
}

// WorkingDir returns the process working directory, or "/" when it cannot be
// determined. Used as the default workspace.
func WorkingDir() string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		return string(filepath.Separator)
	}
	return wd
}

var commandNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// deniedFlags lists per-command arguments that turn an otherwise read-only
// tool back into arbitrary execution or arbitrary writes. The sandbox already
// contains the damage; this keeps the shell tool honest at the host boundary
// too, which matters for the AllowUnsandboxedHostExecution path.
var deniedFlags = map[string][]string{
	"find": {"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprintf", "-fls"},
	"grep": {"-f", "--file"},
	"sort": {"-o", "--output"},
}

// ValidateArgv rejects a command name or argument vector that the tool layer
// must never pass to an exec call.
//
// The command must be a bare binary NAME: allowing "/usr/bin/env" or
// "./script" would let an allow-listed name address an arbitrary executable.
// Arguments are bounded in count and length (an unbounded argv is a cheap
// memory amplifier), must be valid UTF-8, must not contain NUL, and must not
// name a per-command flag that re-grants execution.
func ValidateArgv(command string, args []string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("sandbox: command is required")
	}
	if strings.ContainsAny(command, "/\\") {
		return fmt.Errorf("sandbox: command %q must be a bare binary name, not a path", command)
	}
	if !commandNameRE.MatchString(command) {
		return fmt.Errorf("sandbox: command %q contains characters that are not permitted in a binary name", command)
	}
	if len(args) > MaxArgs {
		return fmt.Errorf("sandbox: %d arguments exceeds the limit of %d", len(args), MaxArgs)
	}
	denied := deniedFlags[command]
	for i, a := range args {
		if len(a) > MaxArgLen {
			return fmt.Errorf("sandbox: argument %d is %d bytes, over the %d-byte limit", i, len(a), MaxArgLen)
		}
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("sandbox: argument %d contains a NUL byte", i)
		}
		if !utf8.ValidString(a) {
			return fmt.Errorf("sandbox: argument %d is not valid UTF-8", i)
		}
		for _, d := range denied {
			if a == d || strings.HasPrefix(a, d+"=") {
				return fmt.Errorf("sandbox: %s %s executes arbitrary commands and is not permitted", command, d)
			}
		}
	}
	return nil
}

// CheckAllowed reports whether command is on the allow-list.
//
//nolint:gocritic // hugeParam: value semantics are deliberate, see Normalize
func (c Config) CheckAllowed(command string) error {
	for _, a := range c.AllowedCommands {
		if a == command {
			return nil
		}
	}
	return fmt.Errorf("sandbox: command %q is not allow-listed", command)
}
