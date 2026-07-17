package ssh

import (
	"regexp"
	"strings"
)

// Read-only command classification for the MCP bridge. When an external LLM
// asks to run a command on a shared session, commands that clearly only READ
// state auto-run; anything else raises an approval modal. This is deliberately
// conservative: when in doubt, NOT read-only (-> prompt). It is a convenience
// gate to cut approval fatigue on obvious reads, never a security boundary on
// its own - the approval modal is the real control.

// readOnlyCommands is the built-in allowlist of leading command tokens that
// only inspect state. A command auto-runs only if EVERY segment of a pipeline
// (split on |, &&, ||, ;) starts with one of these AND the whole line carries
// no mutation markers (see mutationMarkers / redirection checks).
var readOnlyCommands = map[string]bool{
	"cat": true, "ls": true, "head": true, "tail": true, "less": true,
	"more": true, "grep": true, "egrep": true, "fgrep": true, "rg": true,
	"find": true, "locate": true, "file": true, "stat": true, "readlink": true,
	"realpath": true, "wc": true, "sort": true, "uniq": true, "cut": true,
	"tr": true, "awk": true, "sed": true, "column": true, "nl": true,
	"journalctl": true, "dmesg": true, "uptime": true, "who": true, "w": true,
	"whoami": true, "id": true, "groups": true, "hostname": true, "uname": true,
	"date": true, "env": true, "printenv": true, "echo": true, "which": true,
	"type": true, "ps": true, "pstree": true, "top": true, "htop": true,
	"free": true, "df": true, "du": true, "lsblk": true, "lsof": true,
	"mount": true, "ss": true, "netstat": true, "ip": true, "ifconfig": true,
	"ping": true, "traceroute": true, "dig": true, "nslookup": true, "host": true,
	"getent": true, "lscpu": true, "lsmod": true, "lspci": true, "lsusb": true,
	"vmstat": true, "iostat": true, "sar": true, "cksum": true, "md5sum": true,
	"sha256sum": true, "tree": true, "basename": true, "dirname": true,
	"true": true, "false": true, "test": true, "sleep": true, "seq": true,
	"tac": true, "rev": true, "cmp": true, "diff": true, "comm": true,
	"strings": true, "hexdump": true, "od": true, "xxd": true, "tty": true,
	"pwd": true, "arch": true, "nproc": true, "cal": true,
}

// subcommandReadOnly gates tools whose read-only-ness depends on the first
// argument (verb). For these, the leading token alone is not enough - the
// second token must be an inspect verb.
var subcommandReadOnly = map[string]map[string]bool{
	"systemctl": {
		"status": true, "show": true, "is-active": true, "is-enabled": true,
		"is-failed": true, "list-units": true, "list-unit-files": true,
		"list-timers": true, "cat": true, "get-default": true,
	},
	"docker": {
		"ps": true, "logs": true, "inspect": true, "images": true, "top": true,
		"stats": true, "port": true, "version": true, "info": true, "events": true,
	},
	"kubectl": {
		"get": true, "describe": true, "logs": true, "top": true,
		"explain": true, "version": true, "api-resources": true,
	},
	"git": {
		"status": true, "log": true, "diff": true, "show": true, "branch": true,
		"remote": true, "config": true, "rev-parse": true, "describe": true,
		"ls-files": true, "blame": true, "tag": true, "reflog": true, "shortlog": true,
	},
	"apt": {"list": true, "show": true, "policy": true, "search": true},
	"apt-cache": {"policy": true, "show": true, "search": true, "showpkg": true, "stats": true},
	"dpkg": {"-l": true, "-L": true, "-s": true, "-S": true, "--list": true, "--status": true},
	"rpm": {"-q": true, "-qa": true, "-qi": true, "-ql": true},
	"snap": {"list": true, "info": true, "version": true},
	"pip": {"list": true, "show": true, "freeze": true},
	"pip3": {"list": true, "show": true, "freeze": true},
	"npm": {"ls": true, "list": true, "view": true, "outdated": true},
}

// mutationTokens are command names that ALWAYS force approval regardless of
// context - even if they somehow slipped through as a "read" they mutate or
// escalate. A pipeline containing any of these is never auto-run.
var mutationTokens = map[string]bool{
	"sudo": true, "su": true, "doas": true, "rm": true, "rmdir": true,
	"mv": true, "cp": true, "dd": true, "tee": true, "install": true,
	"mkdir": true, "touch": true, "ln": true, "chmod": true, "chown": true,
	"chgrp": true, "truncate": true, "shred": true, "kill": true, "killall": true,
	"pkill": true, "reboot": true, "shutdown": true, "halt": true, "poweroff": true,
	"mkfs": true, "fdisk": true, "parted": true, "wipefs": true, "swapon": true,
	"swapoff": true, "modprobe": true, "insmod": true, "rmmod": true,
	"iptables": true, "nft": true, "route": true, "sysctl": true,
	"useradd": true, "userdel": true, "usermod": true, "passwd": true,
	"crontab": true, "at": true, "systemd-run": true,
	"curl": true, "wget": true, // fetch+execute risk; make the user look
	"nc": true, "ncat": true, "socat": true,
	"eval": true, "exec": true, "source": true,
	"bash": true, "sh": true, "zsh": true, "fish": true, "python": true,
	"python3": true, "perl": true, "ruby": true, "node": true, "php": true,
	"vi": true, "vim": true, "nano": true, "emacs": true, "ed": true,
}

// IsReadOnly reports whether command is safe to auto-run without an approval
// modal. extra is the user-configured additional allowlist of leading tokens
// (from the mcp_readonly_extra setting); it can widen the allowlist but never
// overrides the mutation blocklist or the structural rejections below.
func IsReadOnly(command string, extra []string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}

	// Structural rejections: any output redirection, command substitution,
	// backgrounding, or process substitution makes intent opaque -> prompt.
	// (`<` input redirection is fine for reads, but `>`/`>>` write files.)
	for _, bad := range []string{">", ">>", "$(", "`", "&", "<(", ">("} {
		if strings.Contains(command, bad) {
			return false
		}
	}

	extraSet := map[string]bool{}
	for _, e := range extra {
		if e = strings.TrimSpace(e); e != "" {
			extraSet[e] = true
		}
	}

	// Split into pipeline/sequence segments; EVERY segment must be read-only.
	segments := splitSegments(command)
	if len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			return false
		}
		cmd := stripEnvAssignments(fields)
		if len(cmd) == 0 {
			// Segment was only VAR=val assignments with no command - that sets
			// state for later; treat as not auto-runnable.
			return false
		}
		lead := cmd[0]

		if mutationTokens[lead] {
			return false
		}
		if extraSet[lead] {
			continue
		}
		if readOnlyCommands[lead] {
			continue
		}
		if verbs, ok := subcommandReadOnly[lead]; ok {
			// Need a subcommand and it must be an inspect verb. Find the first
			// non-flag-ish token after the command for tools like git/systemctl;
			// for dpkg/rpm the verb IS a flag, so just check any token matches.
			if segmentSubcommandOK(cmd[1:], verbs) {
				continue
			}
			return false
		}
		return false
	}
	return true
}

// splitSegments breaks a command line on the shell operators that chain
// separate commands: |, ||, &&, ;. Quote handling is intentionally minimal -
// a read-only classifier errs toward "not read-only", and any exotic quoting
// that hides an operator just means we prompt, which is the safe default.
func splitSegments(command string) []string {
	// Normalise the two-char operators to a single sentinel first, then split
	// on the sentinel and the single-char separators.
	repl := strings.NewReplacer("&&", "\x00", "||", "\x00", "|", "\x00", ";", "\x00")
	parts := strings.Split(repl.Replace(command), "\x00")
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stripEnvAssignments drops leading VAR=value tokens (env prefixes) so
// `FOO=bar ls` classifies on `ls`.
func stripEnvAssignments(fields []string) []string {
	i := 0
	for i < len(fields) && isEnvAssignment(fields[i]) {
		i++
	}
	return fields[i:]
}

func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	name := tok[:eq]
	for _, r := range name {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// segmentSubcommandOK checks whether the first meaningful token in args is an
// approved inspect verb. For flag-style verbs (dpkg -l) the verb is the first
// token; for word verbs (git status) skip leading global flags is unnecessary
// here because git's read verbs come first in practice - if a global flag
// precedes the verb we conservatively return false (prompt).
func segmentSubcommandOK(args []string, verbs map[string]bool) bool {
	if len(args) == 0 {
		return false
	}
	return verbs[args[0]]
}

// ---- Catastrophic-command classifier for the MCP "YOLO" grant ----
//
// IsDangerous is a HARD safety net: under the read-run-yolo grant the LLM's
// writes auto-run WITHOUT a per-command prompt, but a command IsDangerous flags
// still raises the approval modal. It is deliberately CATASTROPHIC-ONLY - it is
// not the broad mutationTokens set (which includes mkdir/touch/git and would
// make YOLO prompt on nearly everything). It targets the handful of commands
// that can destroy a host or take it offline in one shot: recursive delete of a
// root-ish path, disk/filesystem wipe, block-device overwrite, mass permission
// change on root, a fork bomb, and power-off. When in doubt it errs toward
// DANGEROUS (prompt), the safe direction. It is a safety net, NOT a complete
// audit - the user is still in control via the grant and the activity log.

// dangerBlockDeviceRedirect catches `> /dev/sda`, `>> /dev/nvme0n1`, etc. -
// overwriting a raw block device. Loop / null / zero / tty are not disks.
var dangerBlockDeviceRedirect = regexp.MustCompile(`>>?\s*/dev/(sd[a-z]|nvme\d|vd[a-z]|hd[a-z]|mmcblk\d|xvd[a-z])`)

// dangerForkBomb catches the classic `:(){ :|:& };:` and minor spacing variants.
var dangerForkBomb = regexp.MustCompile(`:\s*\(\s*\)\s*\{.*:\s*\|\s*:\s*&`)

// dangerDDToDevice catches `dd ... of=/dev/sdX` (writing an image straight onto
// a disk). `of=./file` or `of=/tmp/x` is fine.
var dangerDDToDevice = regexp.MustCompile(`\bof=/dev/(sd[a-z]|nvme\d|vd[a-z]|hd[a-z]|mmcblk\d|xvd[a-z])`)

// rootishTarget reports whether a token is a path so broad that a recursive
// operation on it is catastrophic: /, /*, ~, $HOME, ., .., a bare *, or a
// top-level system dir. Trailing slashes are ignored.
func rootishTarget(tok string) bool {
	t := strings.TrimRight(strings.TrimSpace(tok), "/")
	switch t {
	case "", "/", "/*", "*", "~", "~/*", "$HOME", "${HOME}", ".", "..", "/.":
		return true
	}
	// Top-level system directories: /etc, /usr, /var, /bin, /boot, /lib, /root,
	// /home, /opt, /sys, /proc, /dev - and their bare wildcard forms.
	sys := map[string]bool{
		"/etc": true, "/usr": true, "/var": true, "/bin": true, "/sbin": true,
		"/boot": true, "/lib": true, "/lib64": true, "/root": true, "/home": true,
		"/opt": true, "/sys": true, "/proc": true, "/dev": true, "/srv": true,
	}
	if sys[t] {
		return true
	}
	if strings.HasSuffix(t, "/*") && sys[strings.TrimSuffix(t, "/*")] {
		return true
	}
	return false
}

// hasRecursiveFlag reports whether any token is a recursive flag (-r, -R,
// --recursive, or a combined short flag containing r like -rf / -fr).
func hasRecursiveFlag(fields []string) bool {
	for _, f := range fields {
		if f == "--recursive" || f == "--recursive=true" {
			return true
		}
		if len(f) >= 2 && f[0] == '-' && f[1] != '-' {
			if strings.ContainsAny(f[1:], "rR") {
				return true
			}
		}
	}
	return false
}

// stripPrivilegePrefix drops a leading sudo/doas (and their flags) so the real
// command underneath is classified: `sudo rm -rf /` classifies on `rm -rf /`.
// `sudo -u www bash` still leaves `bash`, which the caller's switch doesn't flag
// as catastrophic on its own - that is fine, IsDangerous is a catastrophic-only
// net, and sudo itself already forces a prompt under the read-run gate's
// IsReadOnly=false path anyway.
func stripPrivilegePrefix(cmd []string) []string {
	if len(cmd) == 0 {
		return cmd
	}
	if cmd[0] != "sudo" && cmd[0] != "doas" {
		return cmd
	}
	i := 1
	// Skip sudo flags and any VAR=val it sets, up to the wrapped command.
	for i < len(cmd) {
		tok := cmd[i]
		if strings.HasPrefix(tok, "-") {
			// -u / -g / -p take an argument; skip it too.
			if tok == "-u" || tok == "-g" || tok == "-p" || tok == "-C" || tok == "-h" {
				i += 2
				continue
			}
			i++
			continue
		}
		if isEnvAssignment(tok) {
			i++
			continue
		}
		break
	}
	return cmd[i:]
}

// nonFlagArgs returns the args that are not flags (don't start with '-').
func nonFlagArgs(fields []string) []string {
	var out []string
	for _, f := range fields {
		if !strings.HasPrefix(f, "-") {
			out = append(out, f)
		}
	}
	return out
}

// IsDangerous reports whether command contains a catastrophic operation that
// must prompt even under the YOLO grant. It splits the line the same way
// IsReadOnly does and flags the command if ANY segment is catastrophic.
func IsDangerous(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}

	// Whole-line patterns (redirects and fork bombs span the raw text).
	if dangerBlockDeviceRedirect.MatchString(command) ||
		dangerForkBomb.MatchString(command) ||
		dangerDDToDevice.MatchString(command) {
		return true
	}

	for _, seg := range splitSegments(command) {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		cmd := stripEnvAssignments(fields)
		cmd = stripPrivilegePrefix(cmd)
		if len(cmd) == 0 {
			continue
		}
		lead := cmd[0]
		args := cmd[1:]

		switch {
		case lead == "rm":
			// Recursive/forced delete of a root-ish or wildcard target.
			if hasRecursiveFlag(args) {
				for _, t := range nonFlagArgs(args) {
					if rootishTarget(t) {
						return true
					}
				}
			}
		case lead == "chmod" || lead == "chown" || lead == "chgrp":
			// Mass recursive permission/ownership change on a root-ish path,
			// or the notorious `chmod -R 000`.
			if hasRecursiveFlag(args) {
				for _, t := range nonFlagArgs(args) {
					if rootishTarget(t) {
						return true
					}
				}
			}
			for _, a := range args {
				if a == "000" {
					if hasRecursiveFlag(args) {
						return true
					}
				}
			}
		case strings.HasPrefix(lead, "mkfs"):
			return true
		case lead == "wipefs" || lead == "blkdiscard" || lead == "fdisk" ||
			lead == "parted" || lead == "sgdisk" || lead == "shred" ||
			lead == "mkswap":
			return true
		case lead == "swapoff":
			for _, a := range args {
				if a == "-a" || a == "--all" {
					return true
				}
			}
		case lead == "shutdown" || lead == "reboot" || lead == "halt" ||
			lead == "poweroff":
			return true
		case lead == "init" || lead == "telinit":
			for _, a := range args {
				if a == "0" || a == "6" {
					return true
				}
			}
		}
	}
	return false
}
