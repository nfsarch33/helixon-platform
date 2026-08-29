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
//   - The sandbox is USABLE. A boundary that also blocks the toolchain it
//     exists to host is not a stricter sandbox, it is a broken one, and it
//     fails in the most expensive way available: as a stream of tool errors
//     that look like the agent wrote bad code. v18779 shipped exactly that —
//     every `go` check inside the container failed on the build cache, the
//     noexec tmpfs and the uid-shifted bind mount — and a 28-mutation suite
//     did not notice, because all eight containment tests asserted that
//     something was BLOCKED and not one asserted that legitimate work
//     SUCCEEDS. The missing test class was the positive control; see
//     podman_toolchain_test.go, which is now as load-bearing as the
//     containment tests next to it. Any change that tightens a control here
//     must keep those green or say out loud what it is giving up.
//
// Podman is the only supported engine.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path"
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
	DefaultImage = "docker.io/library/golang:1.27-bookworm"
	// DefaultNetwork disables networking entirely.
	DefaultNetwork = "none"
	// DefaultUser is nobody:nogroup — non-root inside the container. It is
	// only used when user-namespace remapping is DISABLED; see UserNSKeepID
	// for why the default path does not set --user at all.
	DefaultUser = "65534:65534"
	// DefaultMemoryLimit caps container memory.
	//
	// It is deliberately larger than a "hello world" needs. The scratch tmpfs
	// is accounted against this same cgroup, and the whole point of the image
	// is to host a Go toolchain whose linker peaks in the hundreds of MiB, so
	// a ceiling that cannot fit GOCACHE plus a link step is not a security
	// control — it is the read-only-cache bug wearing a different hat, and it
	// fails the same way: as an unexplained tool error the agent gets blamed
	// for. Lower it per agent when the workload is known to be smaller.
	DefaultMemoryLimit = "2g"
	// DefaultPidsLimit caps the process count (fork-bomb ceiling).
	DefaultPidsLimit = 256
	// DefaultTmpfsSize bounds the scratch tmpfs mounted at /tmp.
	//
	// /tmp holds HOME, GOCACHE and GOPATH (see DefaultToolchainEnv), so this
	// has to be big enough for a build cache and a module cache. It is charged
	// against DefaultMemoryLimit; raise both together or neither.
	DefaultTmpfsSize = "256m"
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

// User-namespace modes. See Config.UserNS.
const (
	// UserNSKeepID runs the container in a user namespace where the invoking
	// (rootless) host user keeps its own uid/gid inside the container.
	//
	// This is the default, and it is a USABILITY control that the v18779
	// hardening was missing. With `--user=65534:65534` and rootless podman,
	// the container process is uid 65534, which rootless podman maps to a
	// SUBORDINATE uid on the host — a different uid from the one that owns the
	// bind-mounted workspace. The result is a sandbox that cannot write to its
	// own workspace: every `go test` inside it failed with a permission error
	// that looked exactly like the agent had written broken code. keep-id maps
	// the container process back to the workspace owner, so the workspace is
	// writable by the process that is supposed to write to it, and nothing
	// else changes: --network=none, --read-only, --cap-drop=ALL,
	// no-new-privileges, the memory/pids ceilings and the noexec tmpfs all
	// still apply.
	//
	// keep-id also makes --user redundant AND non-root by construction: podman
	// refuses keep-id for a container created by root, and otherwise runs as
	// the invoking user's own (non-root) uid. The same mechanism is used by
	// this fleet's node-exporter quadlet for the same reason.
	UserNSKeepID = "keep-id"
	// UserNSDisabled emits no --userns flag and falls back to --user. It
	// exists for a ROOTFUL engine, where podman rejects keep-id outright. A
	// rootful engine has no uid-shifting problem to solve, so the classic
	// nobody:nogroup user is the right answer there.
	UserNSDisabled = "disabled"
	// DefaultUserNS is the mode a config gets when it does not choose one.
	DefaultUserNS = UserNSKeepID
)

// WorkspaceScratchDir is the workspace-relative directory the sandbox exports
// as GOTMPDIR and creates before every run.
//
// It has to live in the WORKSPACE, not on /tmp: `go test` links a test binary
// into TMPDIR and then EXECUTES it, and the scratch tmpfs is mounted noexec —
// correctly, and we are not giving that up. The workspace bind mount is the
// only writable, exec-capable location inside the container, so that is where
// the test binary goes.
//
// The leading dot is load-bearing: the go tool skips directories beginning
// with "." when it expands `./...`, so the scratch directory cannot turn into
// a phantom package in the very builds it exists to support.
const WorkspaceScratchDir = ".gotmp"

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
	// UserNS selects the user-namespace mode: UserNSKeepID (default) or
	// UserNSDisabled. See those constants for the reasoning.
	UserNS string
	// User is the uid:gid the container process runs as. Must not be root.
	//
	// It applies ONLY when UserNS is UserNSDisabled. Under keep-id podman
	// already runs the container as the invoking user, and pinning a second,
	// unrelated uid on top is what made the workspace unwritable in v18779 —
	// so setting both is REJECTED at validation rather than silently
	// reconciled. A config that says two incompatible things about identity
	// should be corrected by the operator, not guessed at by the sandbox.
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
	//
	// Normalize merges DefaultToolchainEnv underneath whatever is set here, so
	// an operator entry always wins and the toolchain still works when nobody
	// has configured anything.
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
	// DenyUnlistedTools used to flip the tool policy default from
	// path-guarded to denied. Deny is now the default (see DefaultPolicy),
	// so setting it changes nothing and clearing it cannot loosen anything:
	// Policy.WithDefault only ever restricts. The field is retained so
	// existing configs keep parsing, and as the hook a future looser default
	// would be tightened through.
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
	if c.UserNS == "" {
		c.UserNS = DefaultUserNS
	}
	// User is only meaningful without keep-id. Defaulting it under keep-id
	// would manufacture the exact conflict Validate rejects, out of a config
	// the operator never wrote.
	if c.UserNS == UserNSDisabled && c.User == "" {
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
	c.Env = c.withToolchainEnv()
	return c
}

// DefaultToolchainEnv is the environment the workspace toolchain needs in
// order to work at all inside the hardened container. Every entry here exists
// because its absence produced a hard failure that read as agent incompetence:
//
//	HOME=/tmp        The container user's passwd HOME is /nonexistent, and the
//	                 root filesystem is read-only, so anything that writes
//	                 under HOME dies with "mkdir /nonexistent: read-only file
//	                 system" before it does any work.
//	GOCACHE=/tmp/... The Go build cache. It only needs to be WRITTEN and read,
//	                 never executed, so the noexec scratch tmpfs is the right
//	                 home for it.
//	GOPATH=/tmp/go   Module cache and friends, same reasoning as GOCACHE.
//	GOTOOLCHAIN=…    "local" whenever the network is off, because a toolchain
//	                 download cannot succeed in a container with no network
//	                 and the attempt buries the real version mismatch.
//	GOTMPDIR=<ws>/…  The one thing that CANNOT live on the tmpfs: `go test`
//	                 links a test binary into TMPDIR and executes it, and the
//	                 tmpfs is noexec. See WorkspaceScratchDir. Only set when
//	                 the workspace is writable — with a read-only workspace
//	                 there is nowhere exec-capable to put a test binary, and
//	                 pointing GOTMPDIR at an unwritable path would trade a
//	                 clear failure for a confusing one.
//
// MEASURED under keep-id, so nobody has to re-derive it. Podman synthesizes a
// passwd entry for the mapped user, and the Go toolchain then resolves its own
// defaults to GOCACHE=/workspace/.cache/go-build and GOPATH=/go. Both are
// wrong in ways that are quiet rather than loud:
//
//   - GOCACHE would land INSIDE the agent's repository. It never breaks a
//     build, it just leaves a build cache in the working tree the agent is
//     reasoning about and committing from.
//   - GOPATH=/go is on the READ-ONLY root, so the first dependency that has to
//     be materialized fails. A module with no dependencies never notices.
//
// Because of that, HOME and the GOCACHE/GOPATH pair are REDUNDANT WITH EACH
// OTHER for a self-contained module: removing HOME alone changes nothing, and
// removing GOCACHE and GOPATH alone changes nothing. Mutation testing
// confirmed both. Removing all three is caught by the workspace-pollution
// positive control, not by a build failure — which is exactly why that control
// exists. GOTOOLCHAIN is defensive in the same way: it changes which ERROR you
// get, never whether a correct workspace builds, so its assertion lives at
// config level only.
//
// The values are returned fresh on every call so a caller cannot mutate the
// defaults for everyone else.
//
//nolint:gocritic // hugeParam: value semantics are deliberate, see Normalize
func (c Config) DefaultToolchainEnv() map[string]string {
	mount := c.WorkspaceMount
	if mount == "" {
		mount = DefaultWorkspaceMount
	}
	env := map[string]string{
		"HOME":    "/tmp",
		"GOCACHE": "/tmp/go-build",
		"GOPATH":  "/tmp/go",
	}
	if c.Network == "none" || c.Network == "" {
		// A container with no network cannot download a toolchain, so
		// GOTOOLCHAIN=auto can only ever fail here — and it fails as an
		// obscure network error instead of "go.mod requires go >= X". Say
		// local so the real problem is the one that gets reported.
		env["GOTOOLCHAIN"] = "local"
	}
	if c.WorkspaceAccess == WorkspaceRW {
		env["GOTMPDIR"] = path.Join(mount, WorkspaceScratchDir)
	}
	return env
}

// withToolchainEnv returns Env with the toolchain defaults filled in
// UNDERNEATH the configured values, so sandbox.env always wins.
//
//nolint:gocritic // hugeParam: value semantics are deliberate, see Normalize
func (c Config) withToolchainEnv() map[string]string {
	env := c.DefaultToolchainEnv()
	for k, v := range c.Env {
		env[k] = v
	}
	return env
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
	switch c.UserNS {
	case UserNSKeepID, UserNSDisabled:
	default:
		return c, fmt.Errorf("sandbox: userns %q is not supported (use %q or %q). Modes that share the host's user namespace are not offered: they would undo the isolation this container exists to provide",
			c.UserNS, UserNSKeepID, UserNSDisabled)
	}
	// REJECT rather than reconcile. Under keep-id podman already runs the
	// container as the invoking user; an explicit --user on top of that is how
	// the sandbox ended up unable to write its own workspace. Guessing which
	// of the two the operator meant would re-introduce exactly the silent
	// mismatch that made this failure so expensive to diagnose.
	if c.UserNS == UserNSKeepID && strings.TrimSpace(c.User) != "" {
		return c, fmt.Errorf("sandbox: user %q is set together with userns %q, and they mean different things about who the container is. keep-id already runs the container as the invoking (non-root) user; either drop sandbox.user, or set sandbox.userns: %q to run a rootful engine as an explicit uid",
			c.User, c.UserNS, UserNSDisabled)
	}
	if c.UserNS == UserNSDisabled && strings.TrimSpace(c.User) == "" {
		return c, fmt.Errorf("sandbox: userns %q requires an explicit non-root sandbox.user; without a user namespace the image's own default user is root", c.UserNS)
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
