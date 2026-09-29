package local

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	sshlayer "ssh-tool/internal/ssh"
)

// statsTimeout bounds one probe. wsl.exe has to reach a VM that is already
// running (the shell holds it up), so a slow answer means something is
// wrong, not that it needs longer.
const statsTimeout = 8 * time.Second

const distroMarker = "__SSHTOOL_DISTRO__"

// HostStats snapshots the machine this shell runs on, in the same shape the
// SSH status readout uses, so the status bar and System status popup need
// no second code path. WSL gets the Linux probe run inside the distro (its
// VM has its own memory and disk); Linux and macOS run the probe here;
// PowerShell and cmd read Windows directly (see stats_windows.go).
func (s *Session) HostStats() (*sshlayer.ServerStats, error) {
	var st *sshlayer.ServerStats
	var err error
	switch {
	case s.Kind == "wsl":
		st, err = wslStats()
	case runtime.GOOS == "windows":
		st, err = windowsStats()
	case runtime.GOOS == "darwin":
		st, err = probeHere(sshlayer.StatsProbeCommandDarwin)
	default:
		st, err = probeHere(sshlayer.StatsProbeCommand)
	}
	if err != nil {
		return nil, err
	}
	if st.Shell == "" {
		st.Shell = s.Display
	}
	return st, nil
}

func probeHere(cmdline string) (*sshlayer.ServerStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", cmdline).Output()
	if len(out) == 0 && err != nil {
		return nil, fmt.Errorf("stats probe: %w", err)
	}
	return sshlayer.ParseServerStats(string(out)), nil
}

// wslStats runs the Linux probe in the default distro - the one a "wsl"
// shell opens - and names the distro for the status bar.
func wslStats() (*sshlayer.ServerStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wsl.exe", "-e", "sh", "-c",
		sshlayer.StatsProbeCommand+`; echo `+distroMarker+`; echo "$WSL_DISTRO_NAME"`)
	hideConsole(cmd)
	out, err := cmd.Output()
	if len(out) == 0 && err != nil {
		return nil, fmt.Errorf("wsl stats probe: %w", err)
	}
	probe, distro, _ := strings.Cut(string(out), distroMarker)
	st := sshlayer.ParseServerStats(probe)
	st.Shell = "WSL"
	if d := strings.TrimSpace(distro); d != "" {
		st.Shell = "WSL " + d
	}
	return st, nil
}

// sysinfoTimeout bounds one System status tab command (ps, systemctl,
// journalctl) on a local shell.
const sysinfoTimeout = 15 * time.Second

// SupportsSysinfo: a WSL shell or a Linux machine can list processes and
// systemd units the way an SSH host does; PowerShell, cmd and macOS cannot.
func (s *Session) SupportsSysinfo() bool {
	return s.Kind == "wsl" || runtime.GOOS == "linux"
}

// RunSysinfo runs one read-only command line where this shell runs (inside
// the WSL distro for a WSL shell) for the System status Processes and
// Services tabs. Output is stdout and stderr together, like the SSH side.
func (s *Session) RunSysinfo(cmdline string) (string, error) {
	if !s.SupportsSysinfo() {
		return "", fmt.Errorf("not available for this shell")
	}
	ctx, cancel := context.WithTimeout(context.Background(), sysinfoTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if s.Kind == "wsl" {
		cmd = exec.CommandContext(ctx, "wsl.exe", "-e", "sh", "-c", cmdline)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdline)
	}
	hideConsole(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
