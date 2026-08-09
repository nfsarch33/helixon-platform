// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
)

// DoctorOptions configures the doctor subcommand.
type DoctorOptions struct {
	ConfigPath string
	Logger     *slog.Logger
	ScriptPath string
}

// DoctorResult is the parsed (best-effort) doctor summary.
type DoctorResult struct {
	ScriptPath string
	ExitCode   int
	Stdout     string
	Stderr     string
	Error      error
}

// Doctor runs the doctor.sh script on this node. MVP: delegates to the
// shell implementation.
func Doctor(ctx context.Context, opts DoctorOptions) (DoctorResult, error) {
	if err := ctx.Err(); err != nil {
		return DoctorResult{}, err
	}
	if opts.Logger == nil {
		return DoctorResult{}, errors.New("logger required")
	}
	if opts.ScriptPath == "" {
		opts.ScriptPath = defaultDoctorScript()
	}

	cmd := exec.CommandContext(ctx, "bash", opts.ScriptPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := DoctorResult{
		ScriptPath: opts.ScriptPath,
		ExitCode:   exitCodeOf(err),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
	}
	if err != nil {
		res.Error = fmt.Errorf("doctor.sh exited %d: %w", res.ExitCode, err)
	}
	opts.Logger.Info("doctor: complete", "exit", res.ExitCode, "script", opts.ScriptPath)
	return res, res.Error
}

// DoctorReport renders a human-readable summary.
func DoctorReport(r DoctorResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "doctor script : %s\n", r.ScriptPath)
	fmt.Fprintf(&b, "exit code     : %d\n", r.ExitCode)
	if r.Error != nil {
		fmt.Fprintf(&b, "error         : %v\n", r.Error)
	}
	if r.Stderr != "" {
		fmt.Fprintf(&b, "stderr        :\n%s\n", strings.TrimSpace(r.Stderr))
	}
	if r.Stdout != "" {
		fmt.Fprintf(&b, "stdout        :\n%s\n", strings.TrimSpace(r.Stdout))
	}
	return b.String()
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func defaultDoctorScript() string {
	switch runtime.GOOS {
	case "windows":
		return `C:\ProgramData\Helixon\doctor.sh`
	default:
		candidates := []string{
			"/usr/local/bin/doctor.sh",
			"/home/jaslian/Code/helixon-monorepo/scripts/fleet/installer/doctor.sh",
		}
		for _, c := range candidates {
			if _, err := exec.LookPath(c); err == nil {
				return c
			}
		}
		return candidates[0]
	}
}
