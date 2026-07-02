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
type ServerStats struct {
	OK          bool    `json:"ok"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	MemUsedPct  float64 `json:"mem_used_pct"`  // 0..100, -1 if unknown
	DiskUsedPct float64 `json:"disk_used_pct"` // 0..100 for /, -1 if unknown
	Users       int     `json:"users"`         // logged-in users, -1 if unknown
}

// statsProbeCommand is a single read-only shell pipeline that gathers all
// four metrics in one round-trip. /proc-first so it doesn't depend on the
// output format of uptime/free (which varies); df -P is POSIX-portable;
// who -q prints a "# users=N" trailer. Each section is guarded with
// `2>/dev/null` and separated by a sentinel so the parser can split cleanly.
// The command is fixed and never interpolates user input.
const statsProbeCommand = `cat /proc/loadavg 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`cat /proc/meminfo 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`df -P / 2>/dev/null; echo __SSHTOOL_SEP__; ` +
	`who -q 2>/dev/null`

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

	// Section 1: /proc/meminfo -> MemTotal / MemAvailable in kB.
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
				}
			case "MemAvailable:":
				if v, err := strconv.ParseFloat(f[1], 64); err == nil {
					avail, haveAvail = v, true
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

	// Section 2: df -P / -> header line, then the root fs row whose 5th
	// field is "NN%". Take the last data row (df -P is one row for a single
	// path but be defensive).
	if len(sections) > 2 {
		for _, line := range strings.Split(sections[2], "\n") {
			f := strings.Fields(line)
			if len(f) < 5 {
				continue
			}
			capField := f[4]
			if !strings.HasSuffix(capField, "%") {
				continue
			}
			if v, err := strconv.ParseFloat(strings.TrimSuffix(capField, "%"), 64); err == nil {
				s.DiskUsedPct = v
				got = true
			}
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
			}
		}
	}

	s.OK = got
	return s
}
