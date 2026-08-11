// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

// ServiceInstaller encapsulates the platform-specific install of the
// fleet-agent as a system service.
//
// On Linux/WSL2 with systemd --user, the install is a no-op: operators
// drop fleet-agent.container into ~/.config/containers/systemd/ and run
// `systemctl --user enable --now helixon-fleet-agent.service`.
//
// On Windows, InstallService wires up golang.org/x/sys/windows/svc and
// registers the agent as "HelixonFleetAgent" under LocalSystem. The
// real implementation is in service_windows.go (gated by build tag).
type ServiceInstaller struct {
	// DisplayName is the user-facing name on Windows.
	DisplayName string
	// BinaryPath is the absolute path to fleet-agent.exe.
	BinaryPath string
	// Args are the command-line args passed to the service (typically
	// `serve --config C:\\ProgramData\\Helixon\\agent.toml`).
	Args []string
}

// InstallService registers the service with the OS service manager.
// Returns ErrNotImplemented on platforms without a service installer
// (Linux uses Podman Quadlet instead).
var ErrNotImplemented = errorString("service installer not implemented on this platform")

type errorString string

func (e errorString) Error() string { return string(e) }

// NewServiceInstaller builds a ServiceInstaller with sane defaults.
// Caller can override DisplayName, BinaryPath, Args before Install().
func NewServiceInstaller(binaryPath string) ServiceInstaller {
	return ServiceInstaller{
		DisplayName: "Helixon Fleet Agent",
		BinaryPath:  binaryPath,
		Args:       []string{"serve", "--config", "/etc/helixon/agent.toml"},
	}
}
