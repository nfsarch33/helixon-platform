package helixon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/sandbox"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
)

// FileConfig is the YAML on-disk shape for a helixon Runtime. It is
// deliberately a flat mirror of RuntimeConfig with string durations so the
// file is human-editable.
//
// Decoding is STRICT (yaml.Decoder.KnownFields): an unknown key is an error,
// not a shrug. Non-strict decoding is how the shipped example's `registra:`
// block sat in this repo being silently discarded — the loader read the file,
// found a key it had no field for, and said nothing. A typo in a security
// setting would have vanished the same way.
//
// Example:
//
//	agent_id: "helixon-claude"
//	system_prompt: "You are a helpful agent."
//	session_dsn: "file:helixon.db?cache=shared&mode=rwc"
//	max_iterations: 25
//	max_tokens: 128000
//	timeout: "5m"
//	heartbeat_every: "60s"
type FileConfig struct {
	AgentID        string                `yaml:"agent_id"`
	SystemPrompt   string                `yaml:"system_prompt"`
	SessionDSN     string                `yaml:"session_dsn"`
	MaxIterations  int                   `yaml:"max_iterations"`
	MaxTokens      int                   `yaml:"max_tokens"`
	Timeout        string                `yaml:"timeout"`
	HeartbeatEvery string                `yaml:"heartbeat_every"`
	Provider       ProviderConfig        `yaml:"provider"`
	Sprintboard    SprintboardFileConfig `yaml:"sprintboard"`
	// Registra mirrors the block that the live fleet-agent config and the
	// shipped example have both carried since v14571. It was never a field,
	// so non-strict decoding threw it away on every load; declaring it is
	// what makes strict decoding safe to turn on.
	Registra   RegistraFileConfig   `yaml:"registra"`
	Sandbox    SandboxFileConfig    `yaml:"sandbox"`
	LoopGuard  LoopGuardFileConfig  `yaml:"loop_guard"`
	Agentrace  AgentraceFileConfig  `yaml:"agentrace"`
	Completion CompletionFileConfig `yaml:"completion"`
	Tickets    TicketsFileConfig    `yaml:"tickets"`
}

// TicketsFileConfig is the YAML shape for the serve-mode ticket poller.
//
// It is the one guardrail block in this file whose default is OFF. Every
// other block defaults on because leaving a boundary off is the dangerous
// choice; here the dangerous choice is the opposite. An agent that pulls its
// own work off a board and executes it unattended is a qualitatively
// different thing from an agent that answers when spoken to, and nobody
// should acquire one by upgrading a binary.
//
//	tickets:
//	  enabled: false            # default; set true to let the agent pull work
//	  interval: 30s
//	  max_backoff: 5m
//	  max_concurrent: 1
//	  ticket_timeout: 15m       # must be >= the agent `timeout`
//	  status: ready
//	  sprint_id: ""
//	  priority_min: 0
//	  labels: []
//	  limit: 0                  # 0 = max_concurrent * 5
type TicketsFileConfig struct {
	Enabled       *bool    `yaml:"enabled"`
	Interval      string   `yaml:"interval"`
	MaxBackoff    string   `yaml:"max_backoff"`
	MaxConcurrent int      `yaml:"max_concurrent"`
	TicketTimeout string   `yaml:"ticket_timeout"`
	Status        string   `yaml:"status"`
	SprintID      string   `yaml:"sprint_id"`
	Labels        []string `yaml:"labels"`
	PriorityMin   int      `yaml:"priority_min"`
	Limit         int      `yaml:"limit"`
}

// SprintboardFileConfig is the YAML shape for sprintboard integration.
type SprintboardFileConfig struct {
	URL          string `yaml:"url"`
	Capabilities string `yaml:"capabilities"`
}

// RegistraFileConfig is the YAML shape for the service-registry wiring.
type RegistraFileConfig struct {
	RegistryPath string `yaml:"registry_path"`
	BridgeURL    string `yaml:"bridge_url"`
	NodeAlias    string `yaml:"node_alias"`
}

// SandboxBindFileConfig is one explicit bind mount.
type SandboxBindFileConfig struct {
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
	Mode      string `yaml:"mode"` // ro (default) | rw
}

// SandboxFileConfig is the YAML shape for the tool-execution boundary.
//
//	sandbox:
//	  enabled: true                # default
//	  image: docker.io/library/golang:1.26-bookworm
//	  network: none                # none (default) | bridge
//	  userns: keep-id              # keep-id (default) | disabled
//	  workspace: /home/agent/work  # defaults to the process working directory
//	  workspace_access: rw         # none | ro | rw (default rw)
//	  memory_limit: 2g
//	  pids_limit: 256
//	  tmpfs_size: 256m
//	  timeout: 2m
//	  max_output_bytes: 65536
//	  env:                         # merged OVER the toolchain defaults below
//	    GOFLAGS: -mod=mod
//	  binds:
//	    - {host: /opt/toolchain, container: /opt/toolchain, mode: ro}
//	  allow_unsandboxed_host_execution: false
//
// Two blocks here are usability controls rather than knobs, and both were
// missing in v18779, which is why every `go` check the agent ran came back as
// an unexplained failure:
//
//   - userns: keep-id maps the container process to the host user that owns
//     the bind-mounted workspace. Under rootless podman, `user: 65534:65534`
//     maps to a SUBORDINATE host uid instead, and the sandbox cannot write to
//     its own workspace. Set `userns: disabled` (and then `user:`) only for a
//     rootful engine, where podman rejects keep-id. Setting `user:` together
//     with keep-id is REJECTED, not reconciled: the two say different things
//     about who the container is, and guessing is how this bug survived.
//   - env: the sandbox pre-populates HOME, GOCACHE, GOPATH and GOTMPDIR (see
//     sandbox.DefaultToolchainEnv) because the container user's HOME is
//     /nonexistent on a read-only root, and because `go test` executes the
//     test binary it links into GOTMPDIR — which therefore cannot live on the
//     noexec scratch tmpfs. Anything set here overrides those defaults.
//
// memory_limit and tmpfs_size are coupled: /tmp holds the build and module
// caches and is charged against the container's memory cgroup, so shrinking
// one without the other turns a build into an OOM kill.
type SandboxFileConfig struct {
	Enabled         *bool                   `yaml:"enabled"`
	Engine          string                  `yaml:"engine"`
	Image           string                  `yaml:"image"`
	Network         string                  `yaml:"network"`
	UserNS          string                  `yaml:"userns"`
	User            string                  `yaml:"user"`
	MemoryLimit     string                  `yaml:"memory_limit"`
	PidsLimit       int                     `yaml:"pids_limit"`
	TmpfsSize       string                  `yaml:"tmpfs_size"`
	Workspace       string                  `yaml:"workspace"`
	WorkspaceAccess string                  `yaml:"workspace_access"`
	WorkspaceMount  string                  `yaml:"workspace_mount"`
	Binds           []SandboxBindFileConfig `yaml:"binds"`
	Env             map[string]string       `yaml:"env"`
	AllowedCommands []string                `yaml:"allowed_commands"`
	Timeout         string                  `yaml:"timeout"`
	MaxOutputBytes  int                     `yaml:"max_output_bytes"`
	// AllowUnsandboxedHostExecution is the explicit, default-off escape
	// hatch. Its name is the documentation.
	AllowUnsandboxedHostExecution bool `yaml:"allow_unsandboxed_host_execution"`
	DenyUnlistedTools             bool `yaml:"deny_unlisted_tools"`
}

// LoopGuardFileConfig is the YAML shape for the repeated-tool-call detector.
type LoopGuardFileConfig struct {
	Enabled   *bool  `yaml:"enabled"`
	Threshold int    `yaml:"threshold"`
	Window    string `yaml:"window"`
}

// AgentraceFileConfig is the YAML shape for the NDJSON tool-call trace.
type AgentraceFileConfig struct {
	Enabled *bool  `yaml:"enabled"`
	LogPath string `yaml:"log_path"`
}

// CompletionFileConfig is the YAML shape for the verifier evidence gate.
type CompletionFileConfig struct {
	Enabled                *bool    `yaml:"enabled"`
	VerifierTool           string   `yaml:"verifier_tool"`
	MutatingTools          []string `yaml:"mutating_tools"`
	MaxConsecutiveFailures int      `yaml:"max_consecutive_failures"`
}

// LoadConfig reads a YAML file at path and returns the parsed RuntimeConfig.
// Empty fields keep their RuntimeConfig zero-value so withDefaults applies.
func LoadConfig(path string) (RuntimeConfig, error) {
	if path == "" {
		return RuntimeConfig{}, errors.New("helixon: empty config path")
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("helixon: read %s: %w", path, err)
	}
	fc, err := DecodeFileConfig(data)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("helixon: parse %s: %w", path, err)
	}
	return fc.ToRuntimeConfig()
}

// DecodeFileConfig parses YAML with strict field checking. An unknown key
// fails the load with the key name and line number rather than disappearing.
func DecodeFileConfig(data []byte) (FileConfig, error) {
	var fc FileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty document is a valid config: everything defaults.
			return FileConfig{}, nil
		}
		return FileConfig{}, err
	}
	return fc, nil
}

// ToRuntimeConfig converts a FileConfig into a RuntimeConfig, parsing the
// duration strings. An empty duration string maps to zero so withDefaults
// can apply the runtime default.
//
//nolint:gocritic // hugeParam: copying the decoded document keeps the runtime config free of aliasing
func (fc FileConfig) ToRuntimeConfig() (RuntimeConfig, error) {
	cfg := RuntimeConfig{
		AgentID:       fc.AgentID,
		SystemPrompt:  fc.SystemPrompt,
		SessionDSN:    fc.SessionDSN,
		MaxIterations: fc.MaxIterations,
		MaxTokens:     fc.MaxTokens,
	}
	if fc.Timeout != "" {
		d, err := time.ParseDuration(fc.Timeout)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("helixon: parse timeout %q: %w", fc.Timeout, err)
		}
		cfg.Timeout = d
	}
	if fc.HeartbeatEvery != "" {
		d, err := time.ParseDuration(fc.HeartbeatEvery)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("helixon: parse heartbeat_every %q: %w", fc.HeartbeatEvery, err)
		}
		cfg.HeartbeatEvery = d
	}
	cfg.Provider = fc.Provider
	cfg.SprintboardURL = fc.Sprintboard.URL
	cfg.SprintboardCapabilities = fc.Sprintboard.Capabilities
	if fc.Provider.TimeoutString != "" {
		d, err := time.ParseDuration(fc.Provider.TimeoutString)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("helixon: parse provider.timeout %q: %w", fc.Provider.TimeoutString, err)
		}
		cfg.Provider.Timeout = d
	}
	sb, err := fc.Sandbox.toConfig()
	if err != nil {
		return RuntimeConfig{}, err
	}
	cfg.Sandbox = sb
	lg, err := fc.LoopGuard.toConfig()
	if err != nil {
		return RuntimeConfig{}, err
	}
	cfg.LoopGuard = lg
	cfg.Agentrace = fc.Agentrace.toConfig()
	cfg.Completion = fc.Completion.toPolicy()
	tk, err := fc.Tickets.toConfig()
	if err != nil {
		return RuntimeConfig{}, err
	}
	cfg.Tickets = tk
	if cfg.Tickets.Enabled && cfg.SprintboardURL == "" {
		return RuntimeConfig{}, errors.New("helixon: tickets.enabled is true but sprintboard.url is empty; " +
			"an autonomous poller with no board to poll would start, log nothing, and do nothing")
	}
	return cfg, nil
}

//nolint:gocritic // hugeParam: value semantics, see ToRuntimeConfig
func (t TicketsFileConfig) toConfig() (TicketPollerConfig, error) {
	cfg := TicketPollerConfig{
		// Note the default: FALSE. boolOr is not used here on purpose —
		// "absent" and "false" mean the same thing for autonomy.
		Enabled:       t.Enabled != nil && *t.Enabled,
		MaxConcurrent: t.MaxConcurrent,
		Status:        t.Status,
		SprintID:      t.SprintID,
		Labels:        t.Labels,
		PriorityMin:   t.PriorityMin,
		Limit:         t.Limit,
	}
	for _, d := range []struct {
		key   string
		raw   string
		field *time.Duration
	}{
		{"tickets.interval", t.Interval, &cfg.Interval},
		{"tickets.max_backoff", t.MaxBackoff, &cfg.MaxBackoff},
		{"tickets.ticket_timeout", t.TicketTimeout, &cfg.TicketTimeout},
	} {
		if d.raw == "" {
			continue
		}
		parsed, err := time.ParseDuration(d.raw)
		if err != nil {
			return TicketPollerConfig{}, fmt.Errorf("helixon: parse %s %q: %w", d.key, d.raw, err)
		}
		*d.field = parsed
	}
	return cfg, nil
}

// boolOr returns *p when set, else def. It is what lets an absent YAML key
// mean "default on" while an explicit `false` still means false. Every caller
// passes true today; the parameter is what documents that "absent" and
// "false" are different answers rather than the same one.
//
//nolint:unparam // def always receives true today, deliberately — see above
func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

//nolint:gocritic // hugeParam: value semantics, see ToRuntimeConfig
func (s SandboxFileConfig) toConfig() (sandbox.Config, error) {
	cfg := sandbox.Config{
		Enabled:                       boolOr(s.Enabled, true),
		Engine:                        s.Engine,
		Image:                         s.Image,
		Network:                       s.Network,
		UserNS:                        s.UserNS,
		User:                          s.User,
		MemoryLimit:                   s.MemoryLimit,
		PidsLimit:                     s.PidsLimit,
		TmpfsSize:                     s.TmpfsSize,
		Workspace:                     s.Workspace,
		WorkspaceAccess:               sandbox.WorkspaceAccess(s.WorkspaceAccess),
		WorkspaceMount:                s.WorkspaceMount,
		Env:                           s.Env,
		AllowedCommands:               s.AllowedCommands,
		MaxOutputBytes:                s.MaxOutputBytes,
		AllowUnsandboxedHostExecution: s.AllowUnsandboxedHostExecution,
		DenyUnlistedTools:             s.DenyUnlistedTools,
	}
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil {
			return sandbox.Config{}, fmt.Errorf("helixon: parse sandbox.timeout %q: %w", s.Timeout, err)
		}
		cfg.Timeout = d
	}
	for _, b := range s.Binds {
		if b.Mode != "" && b.Mode != "ro" && b.Mode != "rw" {
			return sandbox.Config{}, fmt.Errorf("helixon: sandbox bind %q: mode %q must be ro or rw", b.Host, b.Mode)
		}
		cfg.Binds = append(cfg.Binds, sandbox.Bind{
			Host:      b.Host,
			Container: b.Container,
			ReadWrite: b.Mode == "rw",
		})
	}
	return cfg, nil
}

func (l LoopGuardFileConfig) toConfig() (LoopGuardConfig, error) {
	cfg := LoopGuardConfig{
		Enabled:   boolOr(l.Enabled, true),
		Threshold: l.Threshold,
		Window:    0,
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = loopguard.DefaultThreshold
	}
	if l.Window != "" {
		d, err := time.ParseDuration(l.Window)
		if err != nil {
			return LoopGuardConfig{}, fmt.Errorf("helixon: parse loop_guard.window %q: %w", l.Window, err)
		}
		cfg.Window = d
	}
	if cfg.Window <= 0 {
		cfg.Window = loopguard.DefaultWindow
	}
	return cfg, nil
}

func (a AgentraceFileConfig) toConfig() AgentraceConfig {
	return AgentraceConfig{
		Enabled: boolOr(a.Enabled, true),
		LogPath: a.LogPath,
	}
}

func (c CompletionFileConfig) toPolicy() agent.CompletionPolicy {
	p := agent.DefaultCompletionPolicy()
	p.Enabled = boolOr(c.Enabled, true)
	if c.VerifierTool != "" {
		p.VerifierTool = c.VerifierTool
	}
	if len(c.MutatingTools) > 0 {
		p.MutatingTools = c.MutatingTools
	}
	if c.MaxConsecutiveFailures > 0 {
		p.MaxConsecutiveFailures = c.MaxConsecutiveFailures
	}
	return p
}

// DefaultRuntimeConfig returns the config an empty YAML document produces:
// every guardrail on, with its default settings.
func DefaultRuntimeConfig() (RuntimeConfig, error) {
	return FileConfig{}.ToRuntimeConfig()
}
