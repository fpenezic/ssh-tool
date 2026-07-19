package ssh

import (
	"fmt"
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
	`nproc 2>/dev/null`

const statsSep = "__SSHTOOL_SEP__"

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
	return parseServerStats(string(out)), nil
}

// parseServerStats splits the probe output on the sentinel and parses each
// section independently. Unknown metrics are set to -1 (pct/users) or left
// at 0 (load). OK is true if at least one section produced a value.
func parseServerStats(out string) *ServerStats {
	s := &ServerStats{MemUsedPct: -1, DiskUsedPct: -1, Users: -1}
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
				Mount:   mount,
				FS:      fsName,
				SizeKB:  size,
				UsedKB:  used,
				AvailKB: avail,
				UsedPct: pct,
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
	case "tmpfs", "devtmpfs", "overlay", "squashfs", "none", "udev":
		return false
	}
	if strings.HasPrefix(fs, "/dev/loop") {
		return false
	}
	for _, p := range []string{"/snap", "/boot/efi", "/run", "/dev", "/sys", "/proc"} {
		if mount == p || strings.HasPrefix(mount, p+"/") {
			return false
		}
	}
	return true
}
