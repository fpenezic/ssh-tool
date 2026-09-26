package cmdpolicy

import (
	"fmt"
	"regexp"
	"strings"
)

// plain commands only read, whatever their arguments. The invariant matters:
// xargs may hand them arbitrary words from stdin, so nothing that turns into a
// writer with the right flag belongs here (sort -o, tail -f, tree -o, less -o,
// xxd in out, file -C all live in rules instead).
var plain = setOf(
	"cat", "tac", "ls", "head", "grep", "egrep", "fgrep", "zgrep", "zcat", "bzcat", "xzcat",
	"stat", "readlink", "realpath", "basename", "dirname", "wc", "cut", "tr", "column", "nl",
	"rev", "cmp", "diff", "comm", "strings", "hexdump", "od", "base64", "jq",
	"md5sum", "sha1sum", "sha256sum", "sha512sum", "cksum",
	"echo", "printf", "true", "false", ":", "test", "[", "seq", "sleep", "expr", "numfmt",
	"uptime", "who", "w", "whoami", "id", "groups", "uname", "arch", "nproc", "cal", "tty",
	"printenv", "locale", "getconf", "getent", "which", "type", "pwd", "cd", "pushd", "popd",
	"dirs", "set", "shopt", "unset", "shift", "read", "mapfile", "readarray", "getopts",
	"exit", "return", "break", "continue", "wait", "umask", "ulimit",
	"ps", "pstree", "pgrep", "pidof", "free", "df", "du", "lsblk", "findmnt", "lsof",
	"netstat", "lscpu", "lsmem", "lsmod", "lspci", "lsusb", "lshw", "dmidecode",
	"vmstat", "iostat", "mpstat", "last", "lastb",
	"host", "dig", "nslookup", "traceroute", "tracepath", "whois",
	"lvs", "vgs", "pvs", "lvdisplay", "vgdisplay", "pvdisplay",
	"dpkg-query", "pveversion", "pvereport", "systemd-detect-virt", "virt-what",
	"mailq",
)

// notReadOnly gives the reason for commands that are known and refused, so the
// caller sees "rm deletes files" instead of "not a known read-only command".
var notReadOnly = map[string]string{
	"rm": "deletes files", "rmdir": "deletes directories", "mv": "moves files",
	"cp": "writes files", "dd": "writes files", "tee": "writes files", "install": "writes files",
	"mkdir": "creates directories", "touch": "writes files", "ln": "creates links",
	"chmod": "changes permissions", "chown": "changes ownership", "chgrp": "changes ownership",
	"truncate": "writes files", "shred": "destroys files", "kill": "signals processes",
	"killall": "signals processes", "pkill": "signals processes", "reboot": "reboots",
	"shutdown": "powers off", "halt": "powers off", "poweroff": "powers off",
	"wget": "downloads to a file", "watch": "never ends", "htop": "needs a terminal",
	"less": "can write a log file (-o); use cat", "more": "needs a terminal; use cat",
	"vi": "is an editor", "vim": "is an editor", "nano": "is an editor",
	"eval": "runs a string as code", "source": "runs a file as code", ".": "runs a file as code",
	"exec": "replaces the shell", "trap": "runs a string as code", "alias": "redefines commands",
	"bash": "runs arbitrary code", "sh": "runs arbitrary code", "zsh": "runs arbitrary code",
	"python": "runs arbitrary code", "python3": "runs arbitrary code", "perl": "runs arbitrary code",
	"ruby": "runs arbitrary code", "node": "runs arbitrary code", "php": "runs arbitrary code",
	"nc": "opens raw connections", "ncat": "opens raw connections", "socat": "opens raw connections",
	"nohup": "writes nohup.out", "chroot": "runs a command as another root", "su": "switches user",
	"strace": "can write a trace file", "systemd-run": "starts units",
}

type rule func(c *checker, name string, args []arg) error

var rules map[string]rule

func init() {
	rules = map[string]rule{
		// Plain commands with one write/never-ends switch.
		"tail":     denyFlags("f", "--follow", "--retry"),
		"sort":     denyFlags("o", "--output"),
		"tree":     denyFlags("o"),
		"file":     denyFlags("C", "--compile"),
		"rg":       denyFlags("", "--pre", "--pre-glob"),
		"ss":       denyFlags("K", "--kill"),
		"sar":      denyFlags("o"),
		"blkid":    denyFlags("g", "--garbage-collect"),
		"lastlog":  denyFlags("CS", "--clear", "--set"),
		"sensors":  denyFlags("s", "--set"),
		"yq":       denyFlags("i", "--inplace"),
		"smartctl": denyFlags("stXoS", "--smart", "--test", "--abort", "--offlineauto", "--saveauto", "--set"),
		"journalctl": denyFlags("f", "--follow", "--rotate", "--vacuum-*", "--flush", "--sync",
			"--relinquish-var", "--smart-relinquish-var", "--setup-keys", "--update-catalog"),
		"dmesg": denyFlags("cCDEnwW", "--clear", "--read-clear", "--console-off",
			"--console-on", "--console-level", "--follow", "--follow-new"),
		"find": denyTokens("-exec", "-execdir", "-ok", "-okdir", "-delete",
			"-fprint", "-fprint0", "-fprintf", "-fls"),
		"uniq":     maxPositionals(1, "in out writes the second file"),
		"xxd":      maxPositionals(1, "in out writes the second file"),
		"ifconfig": maxPositionals(1, "with more than an interface name changes it"),
		"sed":      ruleSed,
		"awk":      ruleAwk, "gawk": ruleAwk, "mawk": ruleAwk, "nawk": ruleAwk,
		"date":     ruleDate,
		"hostname": ruleHostname,
		"mount":    ruleMount,
		"top":      requireFlag("b", "needs -b (batch mode) without a terminal"),
		"ping":     requirePrefix([]string{"-c", "-w"}, "needs -c or -w or it never ends"),
		"sysctl":   ruleSysctl,
		"ip":       ruleIP,
		"curl":     ruleCurl,
		"crontab":  ruleCrontab,
		"iptables": ruleIptables, "ip6tables": ruleIptables,
		"iptables-legacy": ruleIptables, "iptables-nft": ruleIptables,
		"iptables-save": denyFlags("f", "--file"), "ip6tables-save": denyFlags("f", "--file"),
		"ethtool":      ruleEthtool,
		"mdadm":        ruleMdadm,
		"firewall-cmd": ruleFirewalld,
		"nginx":        allowOnly([]string{"-t", "-T", "-v", "-V", "-q", "-c", "-p", "-g"}, []string{"-t", "-T", "-v", "-V"}, "-c", "-p", "-g"),
		"sshd":         allowOnly([]string{"-t", "-T", "-f", "-C", "-G", "-V"}, []string{"-t", "-T", "-G", "-V"}, "-f", "-C"),
		"haproxy":      allowOnly([]string{"-c", "-f", "-V", "-v", "-vv", "-q"}, []string{"-c", "-v", "-vv"}, "-f"),
		"postqueue":    allowOnly([]string{"-p", "-j"}, []string{"-p", "-j"}),
		"apachectl":    ruleApachectl, "apache2ctl": ruleApachectl,
		"rpm": ruleRpm,
		"dpkg": firstArgIn("-l", "-L", "-s", "-S", "-p", "--list", "--listfiles", "--status",
			"--search", "--print-avail", "--get-selections", "--print-architecture",
			"--print-foreign-architectures", "-V", "--verify", "--audit", "-C", "--version"),

		// Subcommand trees. A nil leaf accepts anything after it; "" means
		// the command may stop at that level.
		"systemctl": verbs(vt{"": nil, "status": nil, "show": nil, "is-active": nil,
			"is-enabled": nil, "is-failed": nil, "is-system-running": nil, "list-units": nil,
			"list-unit-files": nil, "list-timers": nil, "list-sockets": nil,
			"list-dependencies": nil, "list-jobs": nil, "list-machines": nil, "cat": nil,
			"get-default": nil, "show-environment": nil, "help": nil},
			"-t", "-p", "-P", "-n", "-H", "-M", "-s", "-o"),
		"docker": ruleDocker, "podman": ruleDocker,
		"crictl": verbsNoFollow(vt{"ps": nil, "pods": nil, "images": nil, "logs": nil,
			"inspect": nil, "inspectp": nil, "inspecti": nil, "stats": nil, "info": nil, "version": nil}),
		"kubectl": verbsNoFollow(vt{"get": nil, "describe": nil, "logs": nil, "top": nil,
			"explain": nil, "version": nil, "api-resources": nil, "api-versions": nil,
			"cluster-info": nil, "events": nil,
			"config":  vt{"view": nil, "get-contexts": nil, "current-context": nil, "get-clusters": nil},
			"auth":    vt{"can-i": nil, "whoami": nil},
			"rollout": vt{"status": nil, "history": nil}},
			"-n", "--namespace", "--context", "--kubeconfig", "--cluster", "--user", "-s", "--server"),
		"helm": verbs(vt{"list": nil, "ls": nil, "status": nil, "get": nil, "history": nil,
			"version": nil, "show": nil, "search": nil, "env": nil},
			"-n", "--namespace", "--kube-context", "--kubeconfig"),
		"git": ruleGit,
		"apt": verbs(vt{"list": nil, "show": nil, "policy": nil, "search": nil,
			"depends": nil, "rdepends": nil}),
		"apt-cache": verbs(vt{"policy": nil, "show": nil, "search": nil, "showpkg": nil,
			"stats": nil, "depends": nil, "rdepends": nil, "pkgnames": nil, "madison": nil}),
		"dnf": verbs(vt{"list": nil, "info": nil, "repolist": nil, "search": nil,
			"provides": nil, "whatprovides": nil, "check-update": nil, "deplist": nil,
			"repoquery": nil, "updateinfo": nil, "history": vt{"": nil, "list": nil, "info": nil}},
			"-c", "-d", "-e", "--releasever"),
		"snap": verbs(vt{"list": nil, "info": nil, "version": nil, "services": nil,
			"changes": nil, "connections": nil}),
		"pip":  verbs(vt{"list": nil, "show": nil, "freeze": nil, "check": nil}),
		"pip3": verbs(vt{"list": nil, "show": nil, "freeze": nil, "check": nil}),
		"npm":  verbs(vt{"ls": nil, "list": nil, "view": nil, "outdated": nil, "explain": nil}),

		"zpool": verbs(vt{"status": nil, "list": nil, "iostat": nil, "get": nil, "history": nil}),
		"zfs":   verbs(vt{"list": nil, "get": nil}),
		"btrfs": all(denyFlags("z", "--reset"), verbs(vt{"": nil, "version": nil,
			"filesystem": vt{"show": nil, "df": nil, "usage": nil, "du": nil},
			"fi":         vt{"show": nil, "df": nil, "usage": nil, "du": nil},
			"subvolume":  vt{"list": nil, "show": nil},
			"device":     vt{"stats": nil, "usage": nil},
			"scrub":      vt{"status": nil},
			"balance":    vt{"status": nil},
			"qgroup":     vt{"show": nil}})),
		"virsh": verbs(vt{"list": nil, "dominfo": nil, "domstate": nil, "dumpxml": nil,
			"nodeinfo": nil, "net-list": nil, "net-info": nil, "net-dumpxml": nil,
			"pool-list": nil, "pool-info": nil, "vol-list": nil, "vol-info": nil,
			"domblklist": nil, "domiflist": nil, "domblkinfo": nil, "domstats": nil,
			"version": nil, "capabilities": nil, "hostname": nil, "uri": nil,
			"sysinfo": nil, "snapshot-list": nil}, "-c", "--connect"),
		"lxc":   verbs(lxdTree),
		"incus": verbs(lxdTree),

		// Proxmox VE / Backup Server.
		"pvesm":        verbs(vt{"status": nil, "list": nil, "path": nil}),
		"qm":           verbs(vt{"list": nil, "status": nil, "config": nil, "pending": nil, "showcmd": nil}),
		"pct":          verbs(vt{"list": nil, "status": nil, "config": nil, "pending": nil, "df": nil}),
		"pvesh":        verbs(vt{"get": nil, "ls": nil, "usage": nil}),
		"pvecm":        verbs(vt{"status": nil, "nodes": nil}),
		"ha-manager":   verbs(vt{"status": nil, "config": nil, "groupconfig": nil}),
		"pve-firewall": verbs(vt{"status": nil, "compile": nil}),
		"pveum": verbs(vt{"user": vt{"list": nil}, "group": vt{"list": nil},
			"role": vt{"list": nil}, "acl": vt{"list": nil}, "pool": vt{"list": nil},
			"realm": vt{"list": nil}}),
		"proxmox-backup-client": verbs(vt{"list": nil, "status": nil, "version": nil,
			"snapshot": vt{"list": nil, "files": nil},
			"catalog":  vt{"dump": nil},
			"task":     vt{"list": nil, "log": nil},
			"key":      vt{"show": nil}}, "--repository"),
		"proxmox-backup-manager": verbs(vt{"versions": nil,
			"datastore":    vt{"list": nil, "show": nil},
			"task":         vt{"list": nil, "log": nil},
			"user":         vt{"list": nil},
			"acl":          vt{"list": nil},
			"remote":       vt{"list": nil, "show": nil},
			"sync-job":     vt{"list": nil, "show": nil},
			"verify-job":   vt{"list": nil, "show": nil},
			"prune-job":    vt{"list": nil, "show": nil},
			"disk":         vt{"list": nil, "smart-attributes": nil},
			"network":      vt{"list": nil, "show": nil},
			"cert":         vt{"info": nil},
			"subscription": vt{"get": nil}}),
		"ceph": all(denyFlags("w", "--watch*"), verbs(vt{"": nil, "status": nil,
			"health": nil, "df": nil, "versions": nil, "version": nil,
			"osd": vt{"tree": nil, "df": nil, "stat": nil, "ls": nil, "dump": nil, "perf": nil,
				"pool": vt{"ls": nil, "get": nil, "stats": nil}},
			"pg":  vt{"stat": nil, "dump": nil, "ls": nil},
			"mon": vt{"stat": nil, "dump": nil},
			"mgr": vt{"stat": nil, "services": nil},
			"fs":  vt{"status": nil, "ls": nil}})),
		"restic": verbs(vt{"snapshots": nil, "stats": nil, "ls": nil, "version": nil,
			"find": nil, "diff": nil, "key": vt{"list": nil}},
			"-r", "--repo", "-p", "--password-file", "--repository-file", "-o", "--option", "--cache-dir"),
		"borg": verbs(vt{"list": nil, "info": nil}),

		// systemd helpers and friends.
		"timedatectl": verbs(vt{"": nil, "status": nil, "show": nil, "timesync-status": nil,
			"show-timesync": nil, "list-timezones": nil}),
		"hostnamectl": verbs(vt{"": nil, "status": nil}),
		"localectl":   verbs(vt{"": nil, "status": nil, "list-locales": nil, "list-keymaps": nil}),
		"loginctl": verbs(vt{"": nil, "list-sessions": nil, "list-users": nil, "list-seats": nil,
			"show-session": nil, "show-user": nil, "show-seat": nil, "session-status": nil,
			"user-status": nil, "seat-status": nil}),
		"networkctl": verbs(vt{"": nil, "list": nil, "status": nil, "lldp": nil, "label": nil}),
		"resolvectl": verbs(vt{"": nil, "status": nil, "query": nil, "statistics": nil,
			"show-cache": nil, "show-server-state": nil}),
		"systemd-analyze": verbs(vt{"": nil, "time": nil, "blame": nil, "critical-chain": nil,
			"verify": nil, "security": nil, "cat-config": nil}),
		"coredumpctl": verbs(vt{"": nil, "list": nil, "info": nil}),
		"chronyc": verbs(vt{"tracking": nil, "sources": nil, "sourcestats": nil,
			"activity": nil, "ntpdata": nil, "serverstats": nil}),
		"certbot": verbs(vt{"certificates": nil}),
		"ufw":     verbs(vt{"status": nil, "show": nil, "version": nil}),
		"nft":     all(denyFlags("fi", "--file", "--interactive"), verbs(vt{"list": nil})),
		"openssl": all(denyTokens("-out", "-keyout", "-writerand", "-sess_out", "-keylogfile"),
			verbs(vt{"x509": nil, "s_client": nil, "version": nil, "verify": nil, "crl": nil,
				"ciphers": nil, "asn1parse": nil, "dgst": nil})),
	}
}

// ---- wrappers: commands that run another command ----

var wrappers map[string]rule

func init() {
	wrappers = map[string]rule{
		"sudo":    wrapSudo,
		"doas":    wrapSudo,
		"timeout": wrapTimeout,
		"nice":    wrapAfterFlags("-n", "--adjustment"),
		"ionice":  all(denyFlags("pPu", "--pid", "--pgid", "--uid"), wrapAfterFlags("-c", "-n", "--class", "--classdata")),
		"stdbuf":  wrapAfterFlags("-o", "-e", "-i", "--output", "--error", "--input"),
		"command": wrapCommand,
		"env":     wrapEnv,
		"xargs":   wrapXargs,
		"time":    all(denyFlags("o", "--output"), wrapAfterFlags("-f", "--format")),
	}
}

// ---- building blocks ----

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func isFlag(a arg) bool { return a.ok && len(a.s) > 1 && a.s[0] == '-' }

// flagHit reports whether a static argument is one of the denied flags:
// a single-dash cluster containing one of the letters in short, or a token
// matching one of long ("--x" exactly, "--x=..." and "--x*" as a prefix).
func flagHit(a arg, short string, long []string) (string, bool) {
	if !isFlag(a) {
		return "", false
	}
	s := a.s
	for _, l := range long {
		if strings.HasSuffix(l, "*") {
			if strings.HasPrefix(s, strings.TrimSuffix(l, "*")) {
				return s, true
			}
			continue
		}
		if s == l || strings.HasPrefix(s, l+"=") {
			return l, true
		}
	}
	if short != "" && !strings.HasPrefix(s, "--") {
		for _, r := range s[1:] {
			if strings.ContainsRune(short, r) {
				return "-" + string(r), true
			}
		}
	}
	return "", false
}

// denyFlags refuses a command carrying any of the flags. An argument whose
// value is unknown ($VAR) might BE that flag, so it is refused too.
func denyFlags(short string, long ...string) rule {
	return func(_ *checker, name string, args []arg) error {
		for _, a := range args {
			if !a.ok {
				if a.nonFlag {
					continue
				}
				return fmt.Errorf("%s has an argument with an expansion that could be a flag", name)
			}
			if f, hit := flagHit(a, short, long); hit {
				return fmt.Errorf("%s %s is not read-only", name, f)
			}
		}
		return nil
	}
}

// denyTokens refuses exact words (find and openssl use single-dash long options).
func denyTokens(tokens ...string) rule {
	set := setOf(tokens...)
	return func(_ *checker, name string, args []arg) error {
		for _, a := range args {
			if !a.ok {
				if a.nonFlag {
					continue
				}
				return fmt.Errorf("%s has an argument with an expansion that could be a flag", name)
			}
			if set[a.s] {
				return fmt.Errorf("%s %s is not read-only", name, a.s)
			}
		}
		return nil
	}
}

func all(rs ...rule) rule {
	return func(c *checker, name string, args []arg) error {
		for _, r := range rs {
			if err := r(c, name, args); err != nil {
				return err
			}
		}
		return nil
	}
}

// positionals returns the non-flag words, skipping the value after each flag
// in valued. Everything after "--" is positional.
func positionals(args []arg, valued map[string]bool) []arg {
	var out []arg
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.ok && a.s == "--" {
			return append(out, args[i+1:]...)
		}
		if isFlag(a) {
			if valued[a.s] {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func maxPositionals(n int, why string) rule {
	return func(_ *checker, name string, args []arg) error {
		if len(positionals(args, nil)) > n {
			return fmt.Errorf("%s %s", name, why)
		}
		return nil
	}
}

func requireFlag(letter, why string) rule {
	return func(_ *checker, name string, args []arg) error {
		for _, a := range args {
			if _, hit := flagHit(a, letter, nil); hit {
				return nil
			}
		}
		return fmt.Errorf("%s %s", name, why)
	}
}

func requirePrefix(prefixes []string, why string) rule {
	return func(_ *checker, name string, args []arg) error {
		for _, a := range args {
			for _, p := range prefixes {
				if a.ok && strings.HasPrefix(a.s, p) {
					return nil
				}
			}
		}
		return fmt.Errorf("%s %s", name, why)
	}
}

func firstArgIn(allowed ...string) rule {
	set := setOf(allowed...)
	return func(_ *checker, name string, args []arg) error {
		if len(args) == 0 || !args[0].ok || !set[args[0].s] {
			return fmt.Errorf("%s is only read-only with one of %s first", name, strings.Join(allowed, " "))
		}
		return nil
	}
}

// allowOnly accepts a command whose flags all come from allowed, which has at
// least one of required, and whose other words are values of valued flags.
func allowOnly(allowed, required []string, valued ...string) rule {
	aset, rset, vset := setOf(allowed...), setOf(required...), setOf(valued...)
	return func(_ *checker, name string, args []arg) error {
		seen := false
		for i := 0; i < len(args); i++ {
			a := args[i]
			if !a.ok || !aset[a.s] {
				return fmt.Errorf("%s is only read-only with %s", name, strings.Join(required, " / "))
			}
			seen = seen || rset[a.s]
			if vset[a.s] {
				i++
			}
		}
		if !seen {
			return fmt.Errorf("%s is only read-only with %s", name, strings.Join(required, " / "))
		}
		return nil
	}
}

// vt is a subcommand tree: each positional word must be a key; a nil value
// accepts whatever follows, and a "" key lets the command end at that level.
type vt map[string]vt

func walkVerbs(name string, tree vt, pos []arg) error {
	node := tree
	for _, p := range pos {
		if !p.ok {
			return fmt.Errorf("%s has a subcommand with an expansion", name)
		}
		next, ok := node[p.s]
		if !ok || p.s == "" {
			return fmt.Errorf("%s %s is not a known read-only subcommand", name, p.s)
		}
		if next == nil {
			return nil
		}
		node = next
	}
	if _, ok := node[""]; ok {
		return nil
	}
	return fmt.Errorf("%s needs a read-only subcommand", name)
}

func verbs(tree vt, valued ...string) rule {
	vset := setOf(valued...)
	return func(_ *checker, name string, args []arg) error {
		return walkVerbs(name, tree, positionals(args, vset))
	}
}

// verbsNoFollow is verbs plus a refusal of -f / --follow (logs that never end).
func verbsNoFollow(tree vt, valued ...string) rule {
	return all(denyFollow, verbs(tree, valued...))
}

func denyFollow(_ *checker, name string, args []arg) error {
	for _, a := range args {
		if a.ok && (a.s == "-f" || a.s == "--follow" || strings.HasPrefix(a.s, "--follow=")) {
			return fmt.Errorf("%s %s never ends", name, a.s)
		}
	}
	return nil
}

var lxdTree = vt{"list": nil, "ls": nil, "info": nil, "version": nil,
	"config":  vt{"show": nil},
	"image":   vt{"list": nil, "ls": nil},
	"network": vt{"list": nil, "show": nil},
	"profile": vt{"list": nil, "show": nil},
	"storage": vt{"list": nil, "show": nil, "volume": vt{"list": nil}}}

// ---- individual rules ----

// sedWrites finds the w/W/e commands and the w/e flags of s///. It is loose on
// purpose: a false hit only means "not read-only".
var sedWrites = regexp.MustCompile(`(^|[;{}\n!0-9$/])\s*[wWe](\s|;|$)|/[gpIiMm0-9]*[we][gpIiMm0-9]*\s*($|[;}\n])|\b[wW]\s+/`)

func ruleSed(_ *checker, name string, args []arg) error {
	check := func(script string) error {
		if sedWrites.MatchString(script) {
			return fmt.Errorf("sed script writes a file or runs a command")
		}
		return nil
	}
	scriptNext, sawScript := false, false
	for _, a := range args {
		switch {
		case scriptNext:
			if !a.ok {
				return fmt.Errorf("sed script has an expansion")
			}
			if err := check(a.s); err != nil {
				return err
			}
			scriptNext, sawScript = false, true
		case !a.ok:
			if !sawScript {
				return fmt.Errorf("sed script has an expansion")
			}
		case a.s == "-e" || a.s == "--expression":
			scriptNext = true
		case strings.HasPrefix(a.s, "--expression="):
			if err := check(strings.TrimPrefix(a.s, "--expression=")); err != nil {
				return err
			}
			sawScript = true
		case isFlag(a):
			if f, hit := flagHit(a, "f", []string{"--file"}); hit {
				return fmt.Errorf("sed %s runs a script file", f)
			}
			if f, hit := flagHit(a, "i", []string{"--in-place"}); hit {
				return fmt.Errorf("sed %s edits files in place", f)
			}
		case !sawScript:
			if err := check(a.s); err != nil {
				return err
			}
			sawScript = true
		}
	}
	return nil
}

// awkRuns finds system(), piped getline/print and output redirection in an
// awk program. `$3 > 90` inside a pattern is a comparison, but after print it
// is a redirect - hence the print-anchored check.
var awkRuns = regexp.MustCompile(`system\s*\(|\|\s*getline|\|&|(print|printf)[^;{}]*[>|]`)

func ruleAwk(_ *checker, name string, args []arg) error {
	progSeen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			if !progSeen {
				return fmt.Errorf("%s program has an expansion", name)
			}
			continue
		}
		switch {
		case a.s == "-f" || strings.HasPrefix(a.s, "--file") || a.s == "-i" || strings.HasPrefix(a.s, "--include") ||
			a.s == "-l" || strings.HasPrefix(a.s, "--load") || strings.HasPrefix(a.s, "--inplace"):
			return fmt.Errorf("%s %s loads code from a file", name, a.s)
		case a.s == "-e" || a.s == "--source":
			if i+1 >= len(args) || !args[i+1].ok || awkRuns.MatchString(args[i+1].s) {
				return fmt.Errorf("%s program runs a command or writes a file", name)
			}
			progSeen = true
			i++
		case a.s == "-v" || a.s == "-F" || a.s == "--assign" || a.s == "--field-separator":
			i++
		case isFlag(a):
		case !progSeen:
			if awkRuns.MatchString(a.s) {
				return fmt.Errorf("%s program runs a command or writes a file", name)
			}
			progSeen = true
		}
	}
	return nil
}

func ruleDate(_ *checker, name string, args []arg) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.ok && (a.s == "-d" || a.s == "--date" || a.s == "-r" || a.s == "--reference" ||
			a.s == "-f" || a.s == "--file") {
			i++ // the value is a date or a file to read, not a new time
			continue
		}
		if !a.ok {
			return fmt.Errorf("date has an argument with an expansion")
		}
		if f, hit := flagHit(a, "s", []string{"--set"}); hit {
			return fmt.Errorf("date %s sets the clock", f)
		}
		if !isFlag(a) && !strings.HasPrefix(a.s, "+") {
			return fmt.Errorf("date %s sets the clock", a.s)
		}
	}
	return nil
}

func ruleHostname(_ *checker, name string, args []arg) error {
	ok := setOf("-f", "-s", "-i", "-I", "-d", "-a", "-A", "-y", "--fqdn", "--long", "--short",
		"--ip-address", "--all-ip-addresses", "--domain", "--alias", "--all-fqdns", "--yp", "--nis")
	for _, a := range args {
		if !a.ok || !ok[a.s] {
			return fmt.Errorf("hostname with an argument sets the hostname")
		}
	}
	return nil
}

func ruleMount(_ *checker, name string, args []arg) error {
	if len(positionals(args, setOf("-t"))) > 0 {
		return fmt.Errorf("mount with a device or path mounts it")
	}
	for _, a := range args {
		if a.ok && isFlag(a) && a.s != "-l" && a.s != "-t" && a.s != "-v" {
			return fmt.Errorf("mount %s is not read-only", a.s)
		}
	}
	return nil
}

func ruleSysctl(_ *checker, name string, args []arg) error {
	for _, a := range args {
		if !a.ok {
			return fmt.Errorf("sysctl has an argument with an expansion")
		}
		if f, hit := flagHit(a, "wpf", []string{"--write", "--load", "--system"}); hit {
			return fmt.Errorf("sysctl %s sets kernel parameters", f)
		}
		if strings.Contains(a.s, "=") {
			return fmt.Errorf("sysctl %s sets a kernel parameter", a.s)
		}
	}
	return nil
}

var ipWriteVerbs = setOf("add", "del", "delete", "change", "replace", "set", "flush",
	"append", "prepend", "exec", "restore", "monitor", "update", "attach", "detach", "save")

func ruleIP(_ *checker, name string, args []arg) error {
	for _, a := range args {
		if !a.ok {
			return fmt.Errorf("ip has an argument with an expansion")
		}
		if a.s == "-b" || a.s == "-batch" || a.s == "-force" {
			return fmt.Errorf("ip %s runs commands from a file", a.s)
		}
		if ipWriteVerbs[a.s] {
			return fmt.Errorf("ip ... %s changes network state", a.s)
		}
	}
	return nil
}

func ruleCurl(_ *checker, name string, args []arg) error {
	for i, a := range args {
		if !a.ok {
			continue // URLs with $VAR are normal; flags come as fixed words
		}
		if a.s == "-X" || a.s == "--request" {
			if i+1 < len(args) && args[i+1].ok && (args[i+1].s == "GET" || args[i+1].s == "HEAD") {
				continue
			}
			return fmt.Errorf("curl %s with a method other than GET/HEAD can change state", a.s)
		}
		if f, hit := flagHit(a, "oOTdFKcDX", []string{"--output", "--output-dir", "--remote-name*",
			"--upload-file", "--data*", "--form*", "--json", "--config", "--cookie-jar",
			"--dump-header", "--trace*", "--stderr", "--create-dirs", "--request"}); hit {
			return fmt.Errorf("curl %s sends data or writes a file", f)
		}
	}
	return nil
}

func ruleCrontab(_ *checker, name string, args []arg) error {
	list := false
	for i := 0; i < len(args); i++ {
		switch {
		case !args[i].ok:
			return fmt.Errorf("crontab has an argument with an expansion")
		case args[i].s == "-l":
			list = true
		case args[i].s == "-u":
			i++
		default:
			return fmt.Errorf("crontab is only read-only as crontab -l")
		}
	}
	if !list {
		return fmt.Errorf("crontab is only read-only as crontab -l")
	}
	return nil
}

func ruleIptables(_ *checker, name string, args []arg) error {
	long := setOf("--list", "--list-rules", "--line-numbers", "--numeric", "--verbose",
		"--exact", "--table", "--wait")
	for _, a := range args {
		if !a.ok {
			return fmt.Errorf("%s has an argument with an expansion", name)
		}
		if !isFlag(a) {
			continue // chain or table name
		}
		if strings.HasPrefix(a.s, "--") {
			if !long[strings.SplitN(a.s, "=", 2)[0]] {
				return fmt.Errorf("%s %s changes rules", name, a.s)
			}
			continue
		}
		for _, r := range a.s[1:] {
			if !strings.ContainsRune("LSnvxtw", r) {
				return fmt.Errorf("%s -%c changes rules", name, r)
			}
		}
	}
	return nil
}

func ruleEthtool(_ *checker, name string, args []arg) error {
	ok := setOf("-i", "-S", "-k", "-g", "-a", "-c", "-l", "-m", "-T", "-P", "-n", "--driver",
		"--statistics", "--module-info", "--phy-statistics")
	for _, a := range args {
		if !a.ok {
			return fmt.Errorf("ethtool has an argument with an expansion")
		}
		if isFlag(a) && !ok[a.s] && !strings.HasPrefix(a.s, "--show-") {
			return fmt.Errorf("ethtool %s changes the interface", a.s)
		}
	}
	return nil
}

func ruleMdadm(c *checker, name string, args []arg) error {
	if err := firstArgIn("--detail", "-D", "--examine", "-E", "--query", "-Q",
		"--detail-platform", "--version", "-V")(c, name, args); err != nil {
		return err
	}
	return denyFlags("CAGfraSRowFI", "--create", "--assemble", "--manage", "--grow", "--fail",
		"--remove", "--add", "--stop", "--run", "--readonly", "--readwrite",
		"--zero-superblock", "--monitor", "--incremental")(c, name, args[1:])
}

func ruleFirewalld(_ *checker, name string, args []arg) error {
	if len(args) == 0 {
		return fmt.Errorf("firewall-cmd needs a --list/--get/--query option")
	}
	for _, a := range args {
		if !a.ok || !isFlag(a) {
			return fmt.Errorf("firewall-cmd %s is not read-only", a.s)
		}
		s := strings.SplitN(a.s, "=", 2)[0]
		switch {
		case strings.HasPrefix(s, "--list-"), strings.HasPrefix(s, "--get-"),
			strings.HasPrefix(s, "--query-"), strings.HasPrefix(s, "--info-"),
			s == "--state", s == "--version", s == "--zone", s == "--permanent":
		default:
			return fmt.Errorf("firewall-cmd %s changes the firewall", a.s)
		}
	}
	return nil
}

func ruleApachectl(_ *checker, name string, args []arg) error {
	ok := setOf("-t", "-S", "-M", "-v", "-V", "-T", "-l", "-L", "-D", "configtest",
		"DUMP_VHOSTS", "DUMP_MODULES", "DUMP_RUN_CFG", "DUMP_INCLUDES")
	if len(args) == 0 {
		return fmt.Errorf("%s with no arguments starts the server", name)
	}
	for _, a := range args {
		if !a.ok || !ok[a.s] {
			return fmt.Errorf("%s %s is not read-only", name, a.s)
		}
	}
	return nil
}

func ruleRpm(c *checker, name string, args []arg) error {
	query := false
	for _, a := range args {
		if a.ok && (strings.HasPrefix(a.s, "-q") || a.s == "--query" || a.s == "-V" ||
			a.s == "--verify" || a.s == "--version") {
			query = true
		}
	}
	if !query {
		return fmt.Errorf("rpm is only read-only with -q / -V")
	}
	return denyTokens("-e", "-i", "-U", "-F", "--erase", "--install", "--upgrade", "--freshen",
		"--import", "--rebuilddb", "--initdb", "--setperms", "--setugids", "--restore",
		"--delsign", "--addsign", "--resign")(c, name, args)
}

var dockerTree = vt{"ps": nil, "logs": nil, "inspect": nil, "images": nil, "top": nil,
	"port": nil, "version": nil, "info": nil, "diff": nil, "history": nil,
	"stats":     nil, // --no-stream enforced below
	"system":    vt{"df": nil, "info": nil},
	"container": vt{"ls": nil, "list": nil, "ps": nil, "inspect": nil, "logs": nil, "top": nil, "port": nil, "diff": nil, "stats": nil},
	"image":     vt{"ls": nil, "list": nil, "inspect": nil, "history": nil},
	"volume":    vt{"ls": nil, "list": nil, "inspect": nil},
	"network":   vt{"ls": nil, "list": nil, "inspect": nil},
	"compose":   vt{"ps": nil, "ls": nil, "logs": nil, "config": nil, "images": nil, "top": nil, "version": nil},
	"pod":       vt{"ps": nil, "ls": nil, "list": nil, "inspect": nil, "top": nil, "stats": nil, "logs": nil},
}

func ruleDocker(c *checker, name string, args []arg) error {
	// -f is --follow after `logs` but a compose file or a filter elsewhere.
	for i, a := range args {
		if a.ok && a.s == "logs" {
			if err := denyFollow(c, name+" logs", args[i+1:]); err != nil {
				return err
			}
			break
		}
	}
	pos := positionals(args, setOf("-H", "--host", "-c", "--context", "--config", "-l", "--log-level",
		"-f", "--file", "-p", "--project-name"))
	if err := walkVerbs(name, dockerTree, pos); err != nil {
		return err
	}
	for i, p := range pos {
		if p.s == "stats" && (i == 0 || pos[i-1].s == "container" || pos[i-1].s == "pod") {
			for _, a := range args {
				if a.ok && a.s == "--no-stream" {
					return nil
				}
			}
			return fmt.Errorf("%s stats needs --no-stream or it never ends", name)
		}
	}
	return nil
}

var gitTree = vt{"status": nil, "log": nil, "diff": nil, "show": nil, "rev-parse": nil,
	"describe": nil, "ls-files": nil, "ls-tree": nil, "blame": nil, "shortlog": nil,
	"cat-file": nil, "rev-list": nil, "grep": nil, "whatchanged": nil, "for-each-ref": nil,
	"branch": nil, "tag": nil, "remote": nil, "config": nil, "reflog": nil, "stash": nil}

func ruleGit(_ *checker, name string, args []arg) error {
	for _, a := range args {
		if !a.ok {
			continue
		}
		if a.s == "-c" || strings.HasPrefix(a.s, "--config-env") || strings.HasPrefix(a.s, "--exec-path") {
			return fmt.Errorf("git %s can make git run a command", a.s)
		}
		if strings.HasPrefix(a.s, "--output") || a.s == "--ext-diff" {
			return fmt.Errorf("git %s is not read-only", a.s)
		}
	}
	pos := positionals(args, setOf("-C", "--git-dir", "--work-tree", "-n", "--max-count", "--format", "--pretty"))
	if err := walkVerbs(name, gitTree, pos); err != nil {
		return err
	}
	verb := pos[0].s
	rest := pos[1:]
	flags := func(ok ...string) error {
		set := setOf(ok...)
		for _, a := range args {
			if isFlag(a) && !set[strings.SplitN(a.s, "=", 2)[0]] {
				return fmt.Errorf("git %s %s is not read-only", verb, a.s)
			}
		}
		return nil
	}
	switch verb {
	case "branch", "tag":
		// A name argument creates a branch/tag unless --list is given.
		listing := false
		for _, a := range args {
			if a.ok && (a.s == "-l" || a.s == "--list" || a.s == "--contains" || a.s == "--merged" || a.s == "--no-merged" || a.s == "--points-at") {
				listing = true
			}
		}
		if len(rest) > 0 && !listing {
			return fmt.Errorf("git %s with a name creates it", verb)
		}
		return flags("-a", "-r", "-v", "-vv", "-l", "--list", "--all", "--remotes", "--verbose",
			"--contains", "--no-contains", "--merged", "--no-merged", "--show-current",
			"--sort", "--format", "--points-at", "-n", "--column", "--no-column", "--color", "--no-color",
			"-C", "--no-pager", "-P", "--git-dir", "--work-tree")
	case "remote":
		if len(rest) > 0 && rest[0].s != "show" && rest[0].s != "get-url" {
			return fmt.Errorf("git remote %s changes remotes", rest[0].s)
		}
	case "config":
		for _, a := range args {
			if a.ok && (strings.HasPrefix(a.s, "--get") || a.s == "--list" || a.s == "-l") {
				return nil
			}
		}
		if len(rest) > 0 && (rest[0].s == "get" || rest[0].s == "list") {
			return nil
		}
		return fmt.Errorf("git config without --get/--list can write config")
	case "reflog", "stash":
		if len(rest) > 0 && rest[0].s != "show" && rest[0].s != "list" {
			return fmt.Errorf("git %s %s changes the repository", verb, rest[0].s)
		}
		if verb == "stash" && len(rest) == 0 {
			return fmt.Errorf("git stash with no subcommand stashes changes")
		}
	}
	return nil
}

// ---- wrapper implementations ----

// wrapAfterFlags skips flags (and the values of valued ones) and classifies
// the rest as the wrapped command. No command left means the wrapper only
// reports something (nice, time with nothing to time) - fine.
func wrapAfterFlags(valued ...string) rule {
	vset := setOf(valued...)
	return func(c *checker, name string, args []arg) error {
		i := 0
		for i < len(args) && isFlag(args[i]) {
			if vset[args[i].s] {
				i++
			}
			i++
		}
		if i >= len(args) {
			return nil
		}
		return c.checkArgv(args[i:])
	}
}

func wrapTimeout(c *checker, name string, args []arg) error {
	vset := setOf("-s", "-k", "--signal", "--kill-after")
	i := 0
	for i < len(args) && isFlag(args[i]) {
		if vset[args[i].s] {
			i++
		}
		i++
	}
	i++ // the duration
	if i >= len(args) {
		return fmt.Errorf("timeout needs a command")
	}
	return c.checkArgv(args[i:])
}

func wrapSudo(c *checker, name string, args []arg) error {
	if !c.opt.AllowSudo {
		return fmt.Errorf("%s is not allowed (allow_sudo is off)", name)
	}
	vset := setOf("-u", "-g", "-p", "-C", "-h", "-U", "-r", "-t", "-D", "-R", "-T")
	i := 0
	for i < len(args) && isFlag(args[i]) {
		if f, hit := flagHit(args[i], "esibS", []string{"--edit", "--shell", "--login", "--background", "--stdin"}); hit {
			return fmt.Errorf("%s %s is not allowed", name, f)
		}
		if vset[args[i].s] {
			i++
		}
		i++
	}
	for i < len(args) && args[i].ok && strings.Contains(args[i].s, "=") && !strings.HasPrefix(args[i].s, "=") {
		if !safeEnvName(strings.SplitN(args[i].s, "=", 2)[0]) {
			return fmt.Errorf("%s sets %s for the command", name, args[i].s)
		}
		i++
	}
	if i >= len(args) {
		return nil // sudo -l, sudo -n -v
	}
	return c.checkArgv(args[i:])
}

func wrapCommand(c *checker, name string, args []arg) error {
	i := 0
	for i < len(args) && isFlag(args[i]) {
		if args[i].s == "-v" || args[i].s == "-V" {
			return nil // lookup only
		}
		i++
	}
	if i >= len(args) {
		return nil
	}
	return c.checkArgv(args[i:])
}

func wrapEnv(c *checker, name string, args []arg) error {
	i := 0
	for i < len(args) && isFlag(args[i]) {
		if f, hit := flagHit(args[i], "S", []string{"--split-string"}); hit {
			return fmt.Errorf("env %s runs a string as a command line", f)
		}
		if args[i].s == "-u" || args[i].s == "-C" || args[i].s == "--unset" || args[i].s == "--chdir" {
			i++
		}
		i++
	}
	for i < len(args) && args[i].ok && strings.Contains(args[i].s, "=") {
		if !safeEnvName(strings.SplitN(args[i].s, "=", 2)[0]) {
			return fmt.Errorf("env sets %s for the command", args[i].s)
		}
		i++
	}
	if i >= len(args) {
		return nil // plain `env` lists the environment
	}
	return c.checkArgv(args[i:])
}

// wrapXargs allows only a plain command: xargs appends words from stdin, and
// a word like `-i` would turn `sed` into an in-place edit.
func wrapXargs(c *checker, name string, args []arg) error {
	vset := setOf("-I", "-d", "-n", "-P", "-L", "-s", "-E", "-a", "--arg-file", "--delimiter",
		"--max-args", "--max-procs", "--max-lines", "--max-chars", "--replace", "--eof")
	i := 0
	for i < len(args) && isFlag(args[i]) {
		if vset[args[i].s] {
			i++
		}
		i++
	}
	if i >= len(args) {
		return nil // defaults to echo
	}
	sub := args[i]
	if !sub.ok || !(plain[sub.s] || c.extra[sub.s]) {
		return fmt.Errorf("xargs may only run a command that is read-only with any arguments, not %s", sub.s)
	}
	return nil
}
