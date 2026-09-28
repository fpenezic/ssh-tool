package ssh

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A fact is one column of the Gather facts report: a short, read-only shell
// snippet whose trimmed stdout is the value. Every snippet is fixed here;
// the only user text that reaches a host is the custom column, which the
// caller has already put through IsReadOnly.
var factCommands = map[string]string{
	"cpu":      `nproc; grep -m1 -E '^(model name|Hardware|cpu model)' /proc/cpuinfo | cut -d: -f2-`,
	"mem":      `awk '/^MemTotal:/{m=$2}/^SwapTotal:/{s=$2}END{print m; print s}' /proc/meminfo`,
	"disks":    `df -Pk`,
	"virt":     `systemd-detect-virt 2>/dev/null || echo unknown`,
	"os":       `. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME" || uname -s`,
	"kernel":   `uname -r`,
	"uptime":   `cut -d' ' -f1 /proc/uptime`,
	"timesync": `timedatectl show -p NTPSynchronized --value 2>/dev/null || echo unknown`,
	// Pending updates from the host's CACHED package metadata: apt -s and
	// dnf -C never touch the network, so a fleet run stays fast and does
	// not hammer the mirrors. The count is as fresh as the last apt update
	// / dnf makecache on that host.
	"updates": `if command -v apt-get >/dev/null 2>&1; then u=$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep '^Inst '); echo "$u" | grep -c '^Inst '; echo "$u" | grep -ci 'security';` +
		` elif command -v dnf >/dev/null 2>&1; then dnf -q -C check-update 2>/dev/null | grep -cE '^[^[:space:]]+\.[^[:space:]]+[[:space:]]'; dnf -q -C updateinfo list --security 2>/dev/null | grep -c .;` +
		` elif command -v zypper >/dev/null 2>&1; then zypper -q --no-refresh lu 2>/dev/null | grep -c '^v '; zypper -q --no-refresh lp --category security 2>/dev/null | grep -c '^[^-+]*|';` +
		` else echo -; echo -; fi`,
	"reboot":  `if [ -f /var/run/reboot-required ]; then echo yes; elif command -v needs-restarting >/dev/null 2>&1; then needs-restarting -r >/dev/null 2>&1; case $? in 0) echo no;; 1) echo yes;; *) echo unknown;; esac; else echo no; fi`,
	"failed":  `command -v systemctl >/dev/null 2>&1 && systemctl --failed --no-legend --plain --no-pager 2>/dev/null | wc -l || echo -`,
	"ips":     `hostname -I 2>/dev/null || ip -o -4 addr show scope global 2>/dev/null | awk '{print $4}'`,
	"gateway": `ip route show default 2>/dev/null | awk '/default/{print $3; exit}'`,
	"dns":     `awk '/^nameserver/{print $2}' /etc/resolv.conf 2>/dev/null`,
	"ports":   `ss -Htln 2>/dev/null | awk '{print $4}' | sed 's/.*://' | sort -un`,
}

// FactKeys lists the known facts in report column order.
var FactKeys = []string{"cpu", "mem", "disks", "virt", "os", "kernel", "uptime", "timesync", "updates", "reboot", "failed", "ips", "gateway", "dns", "ports"}

const factMarker = "__SSHTOOL_FACT__"

// BuildFactsCommand joins the chosen facts into one script. Each section is
// announced by a marker line so a fact that prints nothing, or fails, is
// still told apart from its neighbours.
func BuildFactsCommand(keys []string, custom string) (string, error) {
	var b strings.Builder
	for _, k := range keys {
		cmd, ok := factCommands[k]
		if !ok {
			return "", fmt.Errorf("unknown fact %q", k)
		}
		fmt.Fprintf(&b, "echo %s%s; ( %s ) 2>/dev/null; ", factMarker, k, cmd)
	}
	if strings.TrimSpace(custom) != "" {
		fmt.Fprintf(&b, "echo %scustom; ( %s ) 2>&1 | head -n 1; ", factMarker, custom)
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("pick at least one fact")
	}
	return b.String() + "true", nil
}

// DiskSize is one real filesystem in the facts report.
type DiskSize struct {
	Mount  string `json:"mount"`
	SizeKB int64  `json:"size_kb"`
}

// HostFacts is one row of the report. Values that do not apply stay at
// their zero value / -1 and the frontend shows a dash.
type HostFacts struct {
	CPUCores  int        `json:"cpu_cores"`
	CPUModel  string     `json:"cpu_model"`
	MemKB     int64      `json:"mem_kb"`
	SwapKB    int64      `json:"swap_kb"`
	Disks     []DiskSize `json:"disks"`
	Virt      string     `json:"virt"`
	OS        string     `json:"os"`
	Kernel    string     `json:"kernel"`
	UptimeSec int64      `json:"uptime_sec"`
	TimeSync  string     `json:"timesync"`
	Updates   int        `json:"updates"`  // -1 unknown
	Security  int        `json:"security"` // -1 unknown
	Reboot    string     `json:"reboot"`   // yes | no | unknown | ""
	Failed    int        `json:"failed"`   // -1 unknown
	IPs       []string   `json:"ips"`
	Gateway   string     `json:"gateway"`
	DNS       []string   `json:"dns"`
	Ports     []int      `json:"ports"`
	Custom    string     `json:"custom"`
}

// ParseFacts turns the script's output into a HostFacts.
func ParseFacts(out string) HostFacts {
	f := HostFacts{Updates: -1, Security: -1, Failed: -1}
	for key, val := range splitFacts(out) {
		lines := nonEmptyLines(val)
		first := ""
		if len(lines) > 0 {
			first = lines[0]
		}
		switch key {
		case "cpu":
			f.CPUCores, _ = strconv.Atoi(first)
			if len(lines) > 1 {
				f.CPUModel = tidyCPUModel(lines[1])
			}
		case "mem":
			f.MemKB, _ = strconv.ParseInt(first, 10, 64)
			if len(lines) > 1 {
				f.SwapKB, _ = strconv.ParseInt(lines[1], 10, 64)
			}
		case "disks":
			for _, l := range lines {
				c := strings.Fields(l)
				if len(c) < 6 || !strings.HasSuffix(c[4], "%") {
					continue
				}
				size, _ := strconv.ParseInt(c[1], 10, 64)
				mount := strings.Join(c[5:], " ")
				if isRealMount(c[0], mount, size) {
					f.Disks = append(f.Disks, DiskSize{Mount: mount, SizeKB: size})
				}
			}
			sort.Slice(f.Disks, func(i, j int) bool { return f.Disks[i].Mount < f.Disks[j].Mount })
		case "virt":
			f.Virt = first
		case "os":
			f.OS = first
		case "kernel":
			f.Kernel = first
		case "uptime":
			if v, err := strconv.ParseFloat(first, 64); err == nil {
				f.UptimeSec = int64(v)
			}
		case "timesync":
			f.TimeSync = first
		case "updates":
			f.Updates = atoiOr(first, -1)
			if len(lines) > 1 {
				f.Security = atoiOr(lines[1], -1)
			}
		case "reboot":
			f.Reboot = first
		case "failed":
			f.Failed = atoiOr(first, -1)
		case "ips":
			for _, l := range lines {
				f.IPs = append(f.IPs, strings.Fields(l)...)
			}
		case "gateway":
			f.Gateway = first
		case "dns":
			f.DNS = lines
		case "ports":
			for _, l := range lines {
				if p, err := strconv.Atoi(l); err == nil {
					f.Ports = append(f.Ports, p)
				}
			}
		case "custom":
			f.Custom = first
		}
	}
	return f
}

func splitFacts(out string) map[string]string {
	res := map[string]string{}
	cur := ""
	var b strings.Builder
	flush := func() {
		if cur != "" {
			res[cur] = b.String()
		}
		b.Reset()
	}
	for _, line := range strings.Split(out, "\n") {
		if k, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), factMarker); ok {
			flush()
			cur = k
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	flush()
	return res
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

// tidyCPUModel drops the vendor noise that makes two identical CPUs read
// differently: "Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz" -> "Xeon Gold 6230".
func tidyCPUModel(m string) string {
	m = strings.TrimSpace(m)
	for _, junk := range []string{"(R)", "(TM)", "(tm)", "Intel ", "AMD ", " CPU", " Processor", " processor"} {
		m = strings.ReplaceAll(m, junk, "")
	}
	if i := strings.Index(m, " @"); i > 0 {
		m = m[:i]
	}
	if i := strings.Index(m, " 64-Core"); i > 0 {
		m = m[:i]
	}
	return strings.Join(strings.Fields(m), " ")
}
