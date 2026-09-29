package ssh

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ServerStats is a one-shot snapshot of a remote host's basic health, shown
// in the status bar for the focused session (MobaXterm-style). Every field
// is best-effort: a host that doesn't answer a given section (network gear,
// a stripped container, a non-Linux box) leaves that metric zeroed and OK
// reflects whether ANY section parsed.
//
// The first block (Load/MemUsedPct/DiskUsedPct/Users) drives the compact
// status-bar readout. The rest is the richer detail the "System status"
// popup shows; all of it is optional and back-compatible - an older frontend
// simply ignores the extra fields.
type ServerStats struct {
	OK          bool    `json:"ok"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	MemUsedPct  float64 `json:"mem_used_pct"`  // 0..100, -1 if unknown
	DiskUsedPct float64 `json:"disk_used_pct"` // 0..100 for /, -1 if unknown
	Users       int     `json:"users"`         // logged-in users, -1 if unknown

	// Detail fields for the popup. Zero / empty / nil when unknown.
	Hostname    string     `json:"hostname"`
	Kernel      string     `json:"kernel"`
	UptimeSec   int64      `json:"uptime_sec"`
	NCPU        int        `json:"ncpu"`
	MemTotalKB  int64      `json:"mem_total_kb"`
	MemAvailKB  int64      `json:"mem_avail_kb"`
	SwapTotalKB int64      `json:"swap_total_kb"`
	SwapFreeKB  int64      `json:"swap_free_kb"`
	UserNames   []string   `json:"user_names"`
	Partitions  []DiskPart `json:"partitions"`

	// Local-shell extras. CPUPct is set where there is no load average
	// (Windows): busy share of all cores over a short sample, -1 elsewhere.
	// Shell names the local shell for the status bar ("WSL Ubuntu-24.04").
	CPUPct float64 `json:"cpu_pct"`
	Shell  string  `json:"shell,omitempty"`

	// FailedUnits counts failed systemd units, -1 where there is no
	// systemctl. FailedUnitNames lists them, so the status bar can leave
	// out the ones the user chose to ignore on this host before it decides
	// whether to show its "N failed" chip.
	FailedUnits     int      `json:"failed_units"`
	FailedUnitNames []string `json:"failed_unit_names"`
}

// DiskPart is one real (non-pseudo) filesystem in the popup's storage list.
// Sizes are in 1024-byte blocks (df -Pk), so the frontend can render absolute
// used/total alongside the percentage.
type DiskPart struct {
	Mount   string  `json:"mount"`
	FS      string  `json:"fs"`
	SizeKB  int64   `json:"size_kb"`
	UsedKB  int64   `json:"used_kb"`
	AvailKB int64   `json:"avail_kb"`
	UsedPct float64 `json:"used_pct"`
	// InodePct is df -Pi's IUse% for the same mount, -1 when the fs does
	// not report inodes (btrfs, vfat and friends print "-").
	InodePct float64 `json:"inode_pct"`
}

// statsProbeCommand is a single read-only shell pipeline that gathers every
// metric in one round-trip. /proc-first so it doesn't depend on the output
// format of uptime/free (which varies); df -Pk is POSIX-portable and lists
// ALL mounts (the parser filters pseudo/temp ones); who -q prints the logged-in
// names plus a "# users=N" trailer. Each section is guarded with `2>/dev/null`
// and separated by a sentinel so the parser can split cleanly. The command is
// fixed and never interpolates user input.
const statsProbeCommand = `cat /proc/loadavg 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`cat /proc/meminfo 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`df -Pk 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`who -q 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`hostname 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`uname -r 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`cat /proc/uptime 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`nproc 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`df -Pi 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`if command -v systemctl >/dev/null 2>&1; then echo __SSHTOOL_SYSTEMD__; systemctl --failed --no-legend --plain --no-pager 2>/dev/null | awk '{print $1}'; fi`

const statsSep = "__SSHTOOL_SEP__"

// StatsProbeCommand is the same probe for a local shell (Linux, WSL), which
// runs it through sh on this machine instead of over a session.
const StatsProbeCommand = statsProbeCommand

// StatsProbeCommandDarwin fills the same sections on macOS, which has no
// /proc: load from vm.loadavg, memory rebuilt as meminfo lines from vm_stat
// (free + inactive + speculative pages = available), uptime from
// kern.boottime. df -Pi is left out: BSD df puts inode columns elsewhere.
const StatsProbeCommandDarwin = `sysctl -n vm.loadavg 2>/dev/null | tr -d '{}'; echo __SSHTOOL_SEP__; ` +
	`vm_stat 2>/dev/null | awk -v pg="$(sysctl -n hw.pagesize)" -v tot="$(sysctl -n hw.memsize)" ` +
	`'/Pages free/{f=$3}/Pages inactive/{i=$3}/Pages speculative/{s=$3} END{printf "MemTotal: %d kB\nMemAvailable: %d kB\n", tot/1024, (f+i+s)*pg/1024}'; echo __SSHTOOL_SEP__; ` +
	`df -Pk 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`who -q 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`hostname 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`uname -r 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`echo $(( $(date +%s) - $(sysctl -n kern.boottime | sed 's/.*sec = \([0-9]*\).*/\1/') )); echo __SSHTOOL_SEP__; ` +
	`sysctl -n hw.ncpu 2>/dev/null`

// ParseServerStats parses the output of either probe command.
func ParseServerStats(out string) *ServerStats { return parseServerStats(out) }

// FetchServerStats runs the probe on a side channel of the given client and
// parses the result. It opens a fresh, non-PTY session so it never touches
// the interactive terminal. Returns OK=false stats (not an error) when the
// host answered but nothing parsed, and an error only when the channel
// itself couldn't be opened / run.
func FetchServerStats(client *ssh.Client) (*ServerStats, error) {
	if client == nil {
		return nil, fmt.Errorf("no live ssh client")
	}
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("stats session: %w", err)
	}
	defer sess.Close()
	// CombinedOutput so a section writing to stderr (unlikely with the
	// 2>/dev/null guards) can't stall; the parser tolerates junk.
	out, err := sess.CombinedOutput(statsProbeCommand)
	if err != nil {
		// A non-zero exit (e.g. df missing) still returns partial output;
		// parse what we got rather than failing outright.
		if len(out) == 0 {
			return nil, fmt.Errorf("stats run: %w", err)
		}
	}
	st := parseServerStats(string(out))
	if !st.OK {
		// The popup then says "no readable stats"; what the host actually
		// sent is the only way to tell a broken section from an empty
		// channel. Logged to the in-app log (Settings -> Logs).
		head := string(out)
		if len(head) > 400 {
			head = head[:400]
		}
		log.Printf("server stats: nothing parsed (%d bytes, run err=%v): %q", len(out), err, head)
	}
	return st, nil
}

// parseServerStats splits the probe output on the sentinel and parses each
// section independently. Unknown metrics are set to -1 (pct/users) or left
// at 0 (load). OK is true if at least one section produced a value.
func parseServerStats(out string) *ServerStats {
	s := &ServerStats{MemUsedPct: -1, DiskUsedPct: -1, Users: -1, CPUPct: -1, FailedUnits: -1}
	sections := strings.Split(out, statsSep)
	got := false

	// Section 0: /proc/loadavg -> "0.12 0.34 0.56 1/234 5678"
	if len(sections) > 0 {
		fields := strings.Fields(sections[0])
		if len(fields) >= 3 {
			l1, e1 := strconv.ParseFloat(fields[0], 64)
			l5, e5 := strconv.ParseFloat(fields[1], 64)
			l15, e15 := strconv.ParseFloat(fields[2], 64)
			if e1 == nil && e5 == nil && e15 == nil {
				s.Load1, s.Load5, s.Load15 = l1, l5, l15
				got = true
			}
		}
	}

	// Section 1: /proc/meminfo -> MemTotal / MemAvailable + Swap, in kB.
	if len(sections) > 1 {
		var total, avail float64
		var haveTotal, haveAvail bool
		for _, line := range strings.Split(sections[1], "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			switch f[0] {
			case "MemTotal:":
				if v, err := strconv.ParseFloat(f[1], 64); err == nil {
					total, haveTotal = v, true
					s.MemTotalKB = int64(v)
				}
			case "MemAvailable:":
				if v, err := strconv.ParseFloat(f[1], 64); err == nil {
					avail, haveAvail = v, true
					s.MemAvailKB = int64(v)
				}
			case "SwapTotal:":
				if v, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					s.SwapTotalKB = v
				}
			case "SwapFree:":
				if v, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					s.SwapFreeKB = v
				}
			}
		}
		if haveTotal && haveAvail && total > 0 {
			used := (1 - avail/total) * 100
			if used < 0 {
				used = 0
			}
			s.MemUsedPct = used
			got = true
		}
	}

	// Section 2: df -Pk -> header line, then one row per mount. Columns:
	// Filesystem 1024-blocks Used Available Capacity Mounted-on. The mount
	// path is the last field but may contain spaces, so join f[5:]. Skip
	// pseudo/temp filesystems (see isRealMount); keep the "/" row's capacity
	// as the compact-readout DiskUsedPct.
	if len(sections) > 2 {
		for _, line := range strings.Split(sections[2], "\n") {
			f := strings.Fields(line)
			if len(f) < 6 {
				continue
			}
			capField := f[4]
			if !strings.HasSuffix(capField, "%") {
				continue // header or garbage
			}
			pct, err := strconv.ParseFloat(strings.TrimSuffix(capField, "%"), 64)
			if err != nil {
				continue
			}
			mount := strings.Join(f[5:], " ")
			fsName := f[0]
			size, _ := strconv.ParseInt(f[1], 10, 64)
			used, _ := strconv.ParseInt(f[2], 10, 64)
			avail, _ := strconv.ParseInt(f[3], 10, 64)
			if mount == "/" {
				// Compact readout always reflects the root fs.
				s.DiskUsedPct = pct
				got = true
			}
			if !isRealMount(fsName, mount, size) {
				continue
			}
			s.Partitions = append(s.Partitions, DiskPart{
				Mount:    mount,
				FS:       fsName,
				SizeKB:   size,
				UsedKB:   used,
				AvailKB:  avail,
				UsedPct:  pct,
				InodePct: -1,
			})
			got = true
		}
	}

	// Section 3: who -q -> a line of usernames then "# users=N".
	if len(sections) > 3 {
		for _, line := range strings.Split(sections[3], "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# users=") {
				if v, err := strconv.Atoi(strings.TrimPrefix(line, "# users=")); err == nil {
					s.Users = v
					got = true
				}
				continue
			}
			if line == "" {
				continue
			}
			// The non-trailer line is space-separated logged-in usernames.
			for _, name := range strings.Fields(line) {
				s.UserNames = append(s.UserNames, name)
			}
		}
	}

	// Section 4: hostname.
	if len(sections) > 4 {
		if h := strings.TrimSpace(sections[4]); h != "" {
			s.Hostname = h
			got = true
		}
	}

	// Section 5: uname -r (kernel).
	if len(sections) > 5 {
		if k := strings.TrimSpace(sections[5]); k != "" {
			s.Kernel = k
			got = true
		}
	}

	// Section 6: /proc/uptime -> "<seconds-up> <idle>"; take the first float.
	if len(sections) > 6 {
		fields := strings.Fields(sections[6])
		if len(fields) >= 1 {
			if up, err := strconv.ParseFloat(fields[0], 64); err == nil && up >= 0 {
				s.UptimeSec = int64(up)
				got = true
			}
		}
	}

	// Section 7: nproc.
	if len(sections) > 7 {
		if n, err := strconv.Atoi(strings.TrimSpace(sections[7])); err == nil && n > 0 {
			s.NCPU = n
			got = true
		}
	}

	// Section 8: df -Pi -> same layout as df -Pk with inode counts. Statfs
	// only, like df -Pk, so it costs nothing extra on a host with millions
	// of small files. Matched to the partitions above by mount path.
	if len(sections) > 8 && len(s.Partitions) > 0 {
		inodes := map[string]float64{}
		for _, line := range strings.Split(sections[8], "\n") {
			f := strings.Fields(line)
			if len(f) < 6 || !strings.HasSuffix(f[4], "%") {
				continue // header, or "-" from a fs without inodes
			}
			if v, err := strconv.ParseFloat(strings.TrimSuffix(f[4], "%"), 64); err == nil {
				inodes[strings.Join(f[5:], " ")] = v
			}
		}
		for i := range s.Partitions {
			if v, ok := inodes[s.Partitions[i].Mount]; ok {
				s.Partitions[i].InodePct = v
			}
		}
	}

	// Section 9: failed unit names after a marker line. No marker means no
	// systemctl, which keeps FailedUnits at -1 (no chip) rather than a
	// false 0.
	if len(sections) > 9 {
		if rest, ok := strings.CutPrefix(strings.TrimLeft(sections[9], "\r\n"), "__SSHTOOL_SYSTEMD__"); ok {
			s.FailedUnitNames = []string{}
			for _, l := range strings.Split(rest, "\n") {
				if u := strings.TrimSpace(l); u != "" {
					s.FailedUnitNames = append(s.FailedUnitNames, u)
				}
			}
			s.FailedUnits = len(s.FailedUnitNames)
		}
	}

	s.OK = got
	return s
}

// isRealMount reports whether a df row describes a real, user-relevant
// filesystem rather than a pseudo/temp one the popup should hide (tmpfs,
// overlay, loop-mounted snaps, the ESP, kernel virtual filesystems).
func isRealMount(fs, mount string, sizeKB int64) bool {
	if sizeKB <= 0 {
		return false
	}
	switch fs {
	case "tmpfs", "devtmpfs", "overlay", "squashfs", "none", "udev", "shm", "nsfs":
		return false
	}
	if strings.HasPrefix(fs, "/dev/loop") {
		return false
	}
	// macOS splits one APFS container into a sealed system snapshot at /
	// plus helper volumes; only Data holds what the user fills.
	if strings.HasPrefix(mount, "/System/Volumes/") && mount != "/System/Volumes/Data" {
		return false
	}
	if fs == "devfs" {
		return false
	}
	for _, p := range []string{"/snap", "/boot/efi", "/run", "/dev", "/sys", "/proc"} {
		if mount == p || strings.HasPrefix(mount, p+"/") {
			return false
		}
	}
	// Container runtimes mount per-container filesystems (shm, overlay
	// layers, pod volumes) below their state dirs, often on the host's own
	// disk; a busy host lists dozens. The state dir itself stays - on its
	// own volume it is a real disk worth watching.
	for _, p := range []string{
		"/var/lib/docker/", "/var/lib/containers/", "/var/lib/kubelet/",
		"/var/lib/containerd/", "/var/lib/rancher/", "/var/lib/lxc/",
	} {
		if strings.HasPrefix(mount, p) {
			return false
		}
	}
	return true
}
