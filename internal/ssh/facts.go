package ssh

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
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
		` elif command -v dnf >/dev/null 2>&1; then dnf -q -C check-update 2>/dev/null | grep -cE '^[^[:space:]]+\.[^[:space:]]+[[:space:]]'; dnf -q -C updateinfo list --security 2>/dev/null | awk '{print $NF}' | sed 's/-[^-]*-[^-]*$//' | sort -u | grep -c .;` +
		` elif command -v zypper >/dev/null 2>&1; then zypper -q --no-refresh lu 2>/dev/null | grep -c '^v '; zypper -q --no-refresh lp --category security 2>/dev/null | grep -c '^[^-+]*|';` +
		` else echo -; echo -; fi`,
	"reboot": `if [ -f /var/run/reboot-required ]; then echo yes; elif command -v needs-restarting >/dev/null 2>&1; then needs-restarting -r >/dev/null 2>&1; case $? in 0) echo no;; 1) echo yes;; *) echo unknown;; esac; else echo no; fi`,
	// "ok" then one failed unit per line; "-" without systemd.
	"failed":  `if command -v systemctl >/dev/null 2>&1; then echo ok; systemctl --failed --no-legend --plain --no-pager 2>/dev/null | awk '{print $1}'; else echo -; fi`,
	"ips":     `hostname -I 2>/dev/null || ip -o -4 addr show scope global 2>/dev/null | awk '{print $4}'`,
	"gateway": `ip route show default 2>/dev/null | awk '/default/{print $3; exit}'`,
	"dns":     `awk '/^nameserver/{print $2}' /etc/resolv.conf 2>/dev/null`,
	"ports":   `ss -Htln 2>/dev/null | awk '{print $4}' | sed 's/.*://' | sort -un`,
	"inodes":  `df -Pi`,
	// Containers through docker, else podman, as the login user: that
	// needs the docker group (or rootless podman, which lists only the
	// user's own containers). "noaccess" when the engine refuses, "none"
	// without one - never a silent empty list.
	"containers": `c=; for x in docker podman; do command -v $x >/dev/null 2>&1 && { c=$x; break; }; done;` +
		` if [ -z "$c" ]; then echo none; elif ! ids=$($c ps -aq 2>/dev/null); then echo "noaccess $c";` +
		` else echo "ok $c"; [ -n "$ids" ] && $c inspect --format '{{.Name}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}|{{.State.ExitCode}}|{{.RestartCount}}|{{.State.StartedAt}}|{{.Config.Image}}|{{index .Config.Labels "com.docker.compose.project"}}' $ids 2>/dev/null; fi`,
	// The last package transaction that upgraded something: apt's history
	// log (rotated copies included) or dnf history, newest first, both
	// readable without root. Printed as "YYYY-MM-DD HH:MM" in the host's
	// time. Without either it falls back to when the package database last
	// changed, as unix seconds - an install counts there too.
	"lastpatch": `if [ -d /var/lib/dpkg/info ]; then` +
		` d=$(zcat -f /var/log/apt/history.log* 2>/dev/null | awk '/^Start-Date:/{d=$2" "substr($3,1,5)} /^Upgrade:/{if(d>b)b=d} END{print b}');` +
		` if [ -n "$d" ]; then echo "$d"; else stat -c %Y $(ls -t /var/lib/dpkg/info/*.list | head -n 1); fi;` +
		` elif command -v rpm >/dev/null 2>&1; then` +
		` d=$(command -v dnf >/dev/null 2>&1 && dnf -q -C history list 2>/dev/null | awk -F'|' 'NF>=5{a=$4; gsub(/ /,"",a); n=split(a,x,","); for(i=1;i<=n;i++) if(x[i]=="U"||x[i]=="Upgrade"||x[i]=="Update"){t=$3; gsub(/^ +| +$/,"",t); print t; exit}}');` +
		` if [ -n "$d" ]; then echo "$d"; else rpm -qa --qf '%{INSTALLTIME}\n' | sort -n | tail -n 1; fi;` +
		` elif [ -f /lib/apk/db/installed ]; then stat -c %Y /lib/apk/db/installed;` +
		` else echo -; fi`,
	// Running kernel, newest kernel image in /boot, the newer of the two by
	// version order, and when that image was installed: its ctime, which
	// the copy cannot carry over (rpm and kernel-install keep the package's
	// mtime, days before the install). That is how long a reboot has waited. Containers and distros whose image names carry
	// no version (Arch) print no newest: unknown, not "up to date".
	"kernelpending": `r=$(uname -r); echo "$r"; n=$(ls /boot 2>/dev/null | sed -n 's/^vmlinuz-//p' | grep -v rescue | grep '[0-9]' | sort -V | tail -n 1);` +
		` if [ -n "$n" ]; then echo "$n"; printf '%s\n%s\n' "$r" "$n" | sort -V | tail -n 1; stat -c %Z "/boot/vmlinuz-$n" 2>/dev/null; fi`,
	// Boots recorded in wtmp over the last 30 days, the current one
	// included. No wtmp (or a last without -s) reads as unknown.
	"reboots": `o=$(last -x -s -30days reboot 2>/dev/null) && echo "$o" | grep -c '^reboot' || echo -`,
}

// FactInfo describes one fact for the Gather facts form and the MCP
// list_facts tool: the one place its label lives.
type FactInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Group string `json:"group"`
}

// FactCatalog is every fact in form and report column order.
var FactCatalog = []FactInfo{
	{"cpu", "CPU cores and model", "Hardware"},
	{"mem", "Memory and swap", "Hardware"},
	{"disks", "Disks (size and use per filesystem)", "Hardware"},
	{"inodes", "Inode use", "Hardware"},
	{"virt", "Virtualization", "Hardware"},
	{"os", "Distro and version", "System"},
	{"kernel", "Kernel", "System"},
	{"uptime", "Uptime", "System"},
	{"timesync", "Time sync", "System"},
	{"containers", "Containers (docker / podman)", "System"},
	{"updates", "Pending updates (and security)", "Maintenance"},
	{"lastpatch", "Last package update", "Maintenance"},
	{"kernelpending", "Newer kernel installed", "Maintenance"},
	{"reboot", "Reboot required", "Maintenance"},
	{"reboots", "Boots in the last 30 days", "Maintenance"},
	{"failed", "Failed systemd units", "Maintenance"},
	{"ips", "IP addresses", "Network"},
	{"gateway", "Default gateway", "Network"},
	{"dns", "DNS resolvers", "Network"},
	{"ports", "Listening TCP ports", "Network"},
}

// FactPreset is a named set of facts. Keys are stable: a folder remembers
// the preset its last run used.
type FactPreset struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Facts []string `json:"facts"`
}

// FactPresets are the built-in presets, in menu order. "all" is filled in
// from the catalog.
var FactPresets = []FactPreset{
	{"sizing", "Sizing (hardware + OS)", []string{"cpu", "mem", "disks", "virt", "os", "kernel", "uptime"}},
	{"patch", "Patch day (updates, reboot, failed units)", []string{"os", "kernel", "uptime", "updates", "lastpatch", "kernelpending", "reboot", "failed"}},
	// Health check: what a recurring report to a customer usually covers.
	{"report", "Health check (patching, capacity, availability)", []string{"os", "kernel", "uptime", "timesync", "updates", "lastpatch", "kernelpending", "reboot", "reboots", "failed", "disks", "inodes", "containers"}},
	{"all", "Everything", nil},
}

// FactKeys lists the known facts in report column order.
var FactKeys = func() []string {
	out := make([]string, len(FactCatalog))
	for i, f := range FactCatalog {
		out[i] = f.Key
	}
	return out
}()

func init() {
	for i := range FactPresets {
		if FactPresets[i].Key == "all" {
			FactPresets[i].Facts = FactKeys
		}
	}
}

// FactCommand returns the shell snippet behind a fact, for showing the
// user exactly what will run.
func FactCommand(key string) (string, bool) {
	c, ok := factCommands[key]
	return c, ok
}

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

// DiskSize is one real filesystem in the facts report. Network marks a
// share mounted from elsewhere (CIFS, NFS, sshfs): its size says nothing
// about the host's own disks.
type DiskSize struct {
	Mount   string `json:"mount"`
	SizeKB  int64  `json:"size_kb"`
	UsedPct int    `json:"used_pct"`
	Network bool   `json:"network,omitempty"`
}

// isNetworkSource: "//server/share" (CIFS/SMB) or "host:/path" /
// "user@host:path" (NFS, sshfs, GlusterFS).
func isNetworkSource(fs string) bool {
	return strings.HasPrefix(fs, "//") || strings.Contains(fs, ":")
}

// Container is one docker / podman container.
type Container struct {
	Name      string `json:"name"`
	State     string `json:"state"`  // running | exited | restarting | created | paused | dead
	Health    string `json:"health"` // healthy | unhealthy | starting | ""
	ExitCode  int    `json:"exit_code"`
	Restarts  int    `json:"restarts"`   // restarts by the engine since the container was created
	StartedAt int64  `json:"started_at"` // unix seconds, 0 never started
	Image     string `json:"image"`
	Project   string `json:"project"` // compose project, "" outside one
}

// MountPct is one filesystem's inode use.
type MountPct struct {
	Mount string `json:"mount"`
	Pct   int    `json:"pct"`
}

// HostFacts is one row of the report. Values that do not apply stay at
// their zero value / -1 and the frontend shows a dash.
type HostFacts struct {
	CPUCores  int        `json:"cpu_cores"`
	CPUModel  string     `json:"cpu_model"`
	MemKB     int64      `json:"mem_kb"`
	SwapKB    int64      `json:"swap_kb"`
	Disks     []DiskSize `json:"disks"`
	Inodes    []MountPct `json:"inodes"`
	Virt      string     `json:"virt"`
	OS        string     `json:"os"`
	Kernel    string     `json:"kernel"`
	UptimeSec int64      `json:"uptime_sec"`
	TimeSync  string     `json:"timesync"`
	Updates   int        `json:"updates"`  // -1 unknown
	Security  int        `json:"security"` // -1 unknown
	Reboot    string     `json:"reboot"`   // yes | no | unknown | ""
	Failed    int        `json:"failed"`   // -1 unknown
	// ContainerAccess: ok | noaccess | none (no docker or podman) | ""
	// (not collected). ContainerEngine is docker or podman.
	ContainerAccess string      `json:"container_access"`
	ContainerEngine string      `json:"container_engine"`
	Containers      []Container `json:"containers"`
	// FailedUnits names the failed units counted in Failed.
	FailedUnits []string `json:"failed_units"`
	// LastPatch: unix seconds of the last upgrade transaction (or, without
	// a history log, the last package change), 0 unknown.
	LastPatch int64 `json:"last_patch"`
	// KernelLatest is the newest kernel in /boot; KernelPending says
	// whether it is newer than the running one (yes | no | unknown | "").
	KernelLatest  string `json:"kernel_latest"`
	KernelPending string `json:"kernel_pending"`
	// KernelLatestAt: unix seconds the newest image was installed, 0 unknown.
	KernelLatestAt int64    `json:"kernel_latest_at"`
	Reboots30d     int      `json:"reboots_30d"` // -1 unknown
	IPs            []string `json:"ips"`
	Gateway        string   `json:"gateway"`
	DNS            []string `json:"dns"`
	Ports          []int    `json:"ports"`
	Custom         string   `json:"custom"`
}

// ParseFacts turns the script's output into a HostFacts.
func ParseFacts(out string) HostFacts {
	f := HostFacts{Updates: -1, Security: -1, Failed: -1, Reboots30d: -1}
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
					f.Disks = append(f.Disks, DiskSize{Mount: mount, SizeKB: size, UsedPct: atoiOr(strings.TrimSuffix(c[4], "%"), 0), Network: isNetworkSource(c[0])})
				}
			}
			sort.Slice(f.Disks, func(i, j int) bool { return f.Disks[i].Mount < f.Disks[j].Mount })
		case "inodes":
			// Same columns as df -Pk with inode counts; btrfs and other
			// filesystems without a fixed inode table print "-" and drop out.
			for _, l := range lines {
				c := strings.Fields(l)
				if len(c) < 6 || !strings.HasSuffix(c[4], "%") {
					continue
				}
				total, _ := strconv.ParseInt(c[1], 10, 64)
				mount := strings.Join(c[5:], " ")
				if isRealMount(c[0], mount, total) && !isNetworkSource(c[0]) {
					f.Inodes = append(f.Inodes, MountPct{Mount: mount, Pct: atoiOr(strings.TrimSuffix(c[4], "%"), 0)})
				}
			}
			sort.Slice(f.Inodes, func(i, j int) bool { return f.Inodes[i].Mount < f.Inodes[j].Mount })
		case "lastpatch":
			f.LastPatch = parsePatchTime(first)
		case "kernelpending":
			f.Kernel = first
			f.KernelPending = "unknown"
			if len(lines) >= 3 {
				f.KernelLatest = lines[1]
				f.KernelPending = "no"
				if lines[2] != first {
					f.KernelPending = "yes"
				}
				if len(lines) >= 4 {
					f.KernelLatestAt, _ = strconv.ParseInt(lines[3], 10, 64)
				}
			}
		case "containers":
			f.ContainerAccess, f.ContainerEngine, f.Containers = parseContainers(lines)
		case "reboots":
			f.Reboots30d = atoiOr(first, -1)
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
			if first == "ok" {
				f.FailedUnits = lines[1:]
				f.Failed = len(f.FailedUnits)
			}
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

// parseContainers reads the containers section: a status line, then one
// "|"-separated inspect line per container.
func parseContainers(lines []string) (access, engine string, list []Container) {
	if len(lines) == 0 {
		return "", "", nil
	}
	head := strings.Fields(lines[0])
	if len(head) == 0 {
		return "", "", nil
	}
	access = head[0]
	if len(head) > 1 {
		engine = head[1]
	}
	if access != "ok" {
		return access, engine, nil
	}
	list = []Container{}
	for _, l := range lines[1:] {
		p := strings.Split(l, "|")
		if len(p) < 8 {
			continue
		}
		c := Container{
			Name: strings.TrimPrefix(p[0], "/"), State: p[1], Health: p[2],
			ExitCode: atoiOr(p[3], 0), Restarts: atoiOr(p[4], 0), Image: p[6], Project: p[7],
		}
		if c.Project == "<no value>" {
			c.Project = ""
		}
		if t, err := time.Parse(time.RFC3339Nano, p[5]); err == nil && t.Year() > 1970 {
			c.StartedAt = t.Unix()
		}
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return access, engine, list
}

// parsePatchTime reads the lastpatch value: unix seconds from the
// fallback, or a history-log "YYYY-MM-DD HH:MM", taken as local time (the
// date is what the report shows). 0 when neither.
func parsePatchTime(v string) int64 {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", strings.Join(strings.Fields(v), " "), time.Local); err == nil {
		return t.Unix()
	}
	return 0
}

// RunningFact names the fact whose marker came last in a partial output:
// the one a timed-out script was still running. "" when none started.
func RunningFact(out string) string {
	i := strings.LastIndex(out, factMarker)
	if i < 0 {
		return ""
	}
	k, _, _ := strings.Cut(out[i+len(factMarker):], "\n")
	return strings.TrimSpace(k)
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
