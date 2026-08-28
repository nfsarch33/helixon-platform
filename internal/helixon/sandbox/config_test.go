package sandbox

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func baseConfig(t *testing.T) Config {
	t.Helper()
	return Config{Enabled: true, Workspace: t.TempDir()}.Normalize(t.TempDir())
}

func TestNormalize_AppliesHardenedDefaults(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	got := Config{Enabled: true}.Normalize(wd)

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"engine", got.Engine, DefaultEngine},
		{"image", got.Image, DefaultImage},
		{"network", got.Network, "none"},
		{"user", got.User, DefaultUser},
		{"memory limit", got.MemoryLimit, DefaultMemoryLimit},
		{"pids limit", got.PidsLimit, DefaultPidsLimit},
		{"tmpfs size", got.TmpfsSize, DefaultTmpfsSize},
		{"workspace mount", got.WorkspaceMount, DefaultWorkspaceMount},
		{"workspace access", got.WorkspaceAccess, WorkspaceRW},
		{"workspace", got.Workspace, wd},
		{"timeout", got.Timeout, DefaultTimeout},
		{"max output bytes", got.MaxOutputBytes, DefaultMaxOutputBytes},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if got.AllowUnsandboxedHostExecution {
		t.Error("host execution must default to OFF")
	}
	if len(got.AllowedCommands) != len(DefaultAllowedCommands) {
		t.Errorf("allowed commands = %v", got.AllowedCommands)
	}
}

// TestDefaultAllowedCommands_ExcludesExecutionPrimitives is the v18779
// hardening assertion: git, go and make each execute arbitrary code, so
// allow-listing them by name allow-listed everything.
func TestDefaultAllowedCommands_ExcludesExecutionPrimitives(t *testing.T) {
	t.Parallel()
	banned := []string{"git", "go", "make", "sh", "bash", "env", "python3", "curl", "wget"}
	for _, b := range banned {
		for _, allowed := range DefaultAllowedCommands {
			if allowed == b {
				t.Errorf("%q must not be on the default shell allow-list: it is a general-purpose execution primitive", b)
			}
		}
	}
}

func TestValidate_TableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(c Config) Config
		wantErr string
	}{
		{name: "hardened default is valid"},
		{
			name:    "docker is refused",
			mutate:  func(c Config) Config { c.Engine = "docker"; return c },
			wantErr: "podman is the only permitted engine",
		},
		{
			name:    "empty image",
			mutate:  func(c Config) Config { c.Image = ""; return c },
			wantErr: "image is required",
		},
		{
			name:    "unknown network mode",
			mutate:  func(c Config) Config { c.Network = "host"; return c },
			wantErr: "is not supported",
		},
		{
			name:    "root user by uid",
			mutate:  func(c Config) Config { c.User = "0:0"; return c },
			wantErr: "requires a non-root user",
		},
		{
			name:    "root user by name",
			mutate:  func(c Config) Config { c.User = "root"; return c },
			wantErr: "requires a non-root user",
		},
		{
			// Anthropic sandbox-runtime rule: filesystem isolation without
			// network isolation permits exfiltration.
			name: "network plus writable workspace is an exfiltration path",
			mutate: func(c Config) Config {
				c.Network = "bridge"
				c.WorkspaceAccess = WorkspaceRW
				return c
			},
			wantErr: "exfiltration path",
		},
		{
			name: "network with a read-only workspace is permitted",
			mutate: func(c Config) Config {
				c.Network = "bridge"
				c.WorkspaceAccess = WorkspaceRO
				return c
			},
		},
		{
			name:    "bogus workspace access",
			mutate:  func(c Config) Config { c.WorkspaceAccess = "readwrite"; return c },
			wantErr: "workspace access",
		},
		{
			name:    "relative workspace mount",
			mutate:  func(c Config) Config { c.WorkspaceMount = "workspace"; return c },
			wantErr: "must be an absolute non-root path",
		},
		{
			name:    "root workspace mount",
			mutate:  func(c Config) Config { c.WorkspaceMount = "/"; return c },
			wantErr: "must be an absolute non-root path",
		},
		{
			name:    "missing workspace directory",
			mutate:  func(c Config) Config { c.Workspace = "/does/not/exist/18779"; return c },
			wantErr: "workspace",
		},
		{
			name:    "bind source that does not exist",
			mutate:  func(c Config) Config { c.Binds = []Bind{{Host: "/nope/18779", Container: "/opt/x"}}; return c },
			wantErr: "bind",
		},
		{
			name:    "bind target at the container root",
			mutate:  func(c Config) Config { c.Binds = []Bind{{Host: "/etc", Container: "/"}}; return c },
			wantErr: "must be an absolute non-root path",
		},
		{
			name:    "non-positive timeout",
			mutate:  func(c Config) Config { c.Timeout = -1; return c },
			wantErr: "timeout must be positive",
		},
		{
			name:    "non-positive output cap",
			mutate:  func(c Config) Config { c.MaxOutputBytes = -1; return c },
			wantErr: "max_output_bytes must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig(t)
			if tt.mutate != nil {
				cfg = tt.mutate(cfg)
			}
			_, err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidate_CanonicalizesBindsAndWorkspace(t *testing.T) {
	t.Parallel()
	cfg := baseConfig(t)
	bindDir := t.TempDir()
	cfg.Binds = []Bind{{Host: bindDir + "/.", Container: "/opt/tools/", ReadWrite: false}}
	got, err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if strings.HasSuffix(got.Binds[0].Host, "/.") {
		t.Errorf("bind host was not canonicalized: %q", got.Binds[0].Host)
	}
	if got.Binds[0].Container != "/opt/tools" {
		t.Errorf("bind container was not cleaned: %q", got.Binds[0].Container)
	}
}

func TestValidateArgv_TableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
		args    []string
		wantErr string
	}{
		{name: "plain command", command: "echo", args: []string{"hello"}},
		{name: "no args", command: "pwd"},
		{name: "empty command", wantErr: "command is required"},
		{name: "absolute path command", command: "/bin/sh", wantErr: "bare binary name"},
		{name: "relative path command", command: "./run.sh", wantErr: "bare binary name"},
		{name: "shell metacharacters in the name", command: "echo;rm", wantErr: "not permitted in a binary name"},
		{name: "find -exec is arbitrary execution", command: "find", args: []string{".", "-exec", "sh", "-c", "id", ";"}, wantErr: "not permitted"},
		{name: "find -execdir", command: "find", args: []string{".", "-execdir", "id", ";"}, wantErr: "not permitted"},
		{name: "find -delete", command: "find", args: []string{".", "-delete"}, wantErr: "not permitted"},
		{name: "find -fprintf writes files", command: "find", args: []string{".", "-fprintf=/tmp/x", "%p"}, wantErr: "not permitted"},
		{name: "grep -f reads a pattern file", command: "grep", args: []string{"-f", "/etc/shadow", "."}, wantErr: "not permitted"},
		{name: "sort -o writes a file", command: "sort", args: []string{"-o", "/etc/passwd"}, wantErr: "not permitted"},
		{name: "find without a dangerous flag is fine", command: "find", args: []string{".", "-name", "*.go"}},
		{name: "NUL byte in an argument", command: "echo", args: []string{"a\x00b"}, wantErr: "NUL byte"},
		{name: "invalid UTF-8", command: "echo", args: []string{string([]byte{0xff, 0xfe})}, wantErr: "valid UTF-8"},
		{name: "argument over the length cap", command: "echo", args: []string{strings.Repeat("x", MaxArgLen+1)}, wantErr: "over the"},
		{name: "too many arguments", command: "echo", args: make([]string, MaxArgs+1), wantErr: "exceeds the limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateArgv(tt.command, tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateArgv(%q, %v) = %v, want nil", tt.command, tt.args, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateArgv(%q, ...) = %v, want an error containing %q", tt.command, err, tt.wantErr)
			}
		})
	}
}

func TestCheckAllowed(t *testing.T) {
	t.Parallel()
	cfg := Config{AllowedCommands: []string{"echo", "ls"}}
	if err := cfg.CheckAllowed("echo"); err != nil {
		t.Fatalf("echo should be allowed: %v", err)
	}
	err := cfg.CheckAllowed("git")
	if err == nil || !strings.Contains(err.Error(), "not allow-listed") {
		t.Fatalf("git must be rejected, got %v", err)
	}
}

func TestWorkingDir_IsAbsolute(t *testing.T) {
	t.Parallel()
	if got := WorkingDir(); got == "" {
		t.Fatal("WorkingDir must never return an empty string")
	}
}

func TestNormalize_PreservesExplicitValues(t *testing.T) {
	t.Parallel()
	in := Config{
		Enabled: true, Engine: "podman", Image: "img:1", Network: "bridge",
		User: "1000:1000", MemoryLimit: "1g", PidsLimit: 8, TmpfsSize: "1m",
		Workspace: "/tmp", WorkspaceAccess: WorkspaceRO, WorkspaceMount: "/w",
		Timeout: time.Second, MaxOutputBytes: 10, AllowedCommands: []string{"echo"},
	}
	got := in.Normalize("/ignored")
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("Normalize altered an explicitly set config:\n got %+v\nwant %+v", got, in)
	}
}
