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
