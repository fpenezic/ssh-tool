package ssh

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ProcInfo is one row of the System status Processes tab.
type ProcInfo struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	Command string  `json:"command"`
}

// topProcLimit is the default row count; the tab lets the user pick 5, 10
// or 20, and maxProcLimit caps whatever arrives. It answers "who is eating
// the CPU", not "show me ps".
const (
	topProcLimit = 10
	maxProcLimit = 50
)

// TopProcesses lists the heaviest processes by "cpu" or "mem". GNU ps only
// (procps --sort); BusyBox ps has neither -o user:N nor --sort and gets a
// plain error instead of a wrong list.
func TopProcesses(client *ssh.Client, by string, limit int) ([]ProcInfo, error) {
	if limit <= 0 {
		limit = topProcLimit
	}
	if limit > maxProcLimit {
		limit = maxProcLimit
	}
	key := "-pcpu"
	if by == "mem" {
		key = "-pmem"
	}
	// A few spare lines: the probe's own ps and head can land on top.
	out, err := runOutput(client, fmt.Sprintf("ps -eo pid=,user:32=,pcpu=,pmem=,args= --sort=%s 2>&1 | head -n %d", key, limit+4))
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("ps: %w", err)
	}
	procs := parsePs(out, limit)
	if len(procs) == 0 {
		return nil, fmt.Errorf("ps gave no usable output: %s", firstLine(out))
	}
	return procs, nil
}

func parsePs(out string, limit int) []ProcInfo {
	var res []ProcInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cmd := strings.Join(f[4:], " ")
		// The probe's own ps / head show up at the top of a CPU sort.
		if strings.HasPrefix(cmd, "ps -eo pid=") || strings.HasPrefix(cmd, "head -n ") {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[2], 64)
		mem, _ := strconv.ParseFloat(f[3], 64)
		res = append(res, ProcInfo{PID: pid, User: f[1], CPU: cpu, Mem: mem, Command: cmd})
		if len(res) == limit {
			break
		}
	}
	return res
}

// UnitInfo is one systemd service in the Services tab.
type UnitInfo struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

// ListServices lists systemd units: every failed one, or services that are
// "running" / "all". Hosts without systemd get an error the tab shows as is.
func ListServices(client *ssh.Client, state string) ([]UnitInfo, error) {
	// Failed lists every unit type, not only services: the status bar
	// chip counts systemctl --failed (mounts and timers included), and the
	// list it opens has to show the same units.
	cmd := "systemctl list-units --no-legend --plain --no-pager"
	switch state {
	case "failed":
		cmd += " --state=failed"
	case "running":
		cmd += " --type=service --state=running"
	default:
		cmd += " --type=service --all"
	}
	out, err := runOutput(client, "command -v systemctl >/dev/null 2>&1 || { echo __NO_SYSTEMD__; exit 0; }; "+cmd+" 2>&1")
	if strings.Contains(out, "__NO_SYSTEMD__") {
		return nil, errors.New("no systemd on this host")
	}
	if err != nil && out == "" {
		return nil, fmt.Errorf("systemctl: %w", err)
	}
	return parseUnits(out), nil
}

func parseUnits(out string) []UnitInfo {
	res := []UnitInfo{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.Contains(f[0], ".") {
			continue
		}
		res = append(res, UnitInfo{Unit: f[0], Load: f[1], Active: f[2], Sub: f[3], Description: strings.Join(f[4:], " ")})
	}
	return res
}

var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9@_.:\\-]+$`)

// UnitLog returns the last lines a unit wrote to the journal. Reading
// another unit's journal can need the adm / systemd-journal group; the
// hint journalctl prints then is returned as a line like any other.
func UnitLog(client *ssh.Client, unit string, n int) ([]string, error) {
	if !unitNameRe.MatchString(unit) {
		return nil, fmt.Errorf("invalid unit name")
	}
	out, err := runOutput(client, fmt.Sprintf("journalctl -u %s -n %d --no-pager -o cat 2>&1", quoteAlways(unit), n))
	if err != nil && out == "" {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// ErrSudoPassword means the action needs root, the login is not root,
// sudo wants a password, and none was supplied.
var ErrSudoPassword = errors.New("sudo password required")

// SignalProcess sends TERM or KILL to pid. It tries as the login user
// first - most kills are of the user's own processes - and escalates only
// when the kernel says no.
func SignalProcess(client *ssh.Client, pid int, signal, password string) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	if signal != "TERM" && signal != "KILL" {
		return fmt.Errorf("unsupported signal %q", signal)
	}
	err := runEscalating(client, fmt.Sprintf("kill -s %s %d", signal, pid), password)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such process") {
		return fmt.Errorf("process %d is no longer running", pid)
	}
	return err
}

// ServiceAction runs systemctl restart / stop / start on one unit.
// --no-ask-password keeps polkit from waiting on a prompt nobody sees.
func ServiceAction(client *ssh.Client, unit, verb, password string) error {
	if !unitNameRe.MatchString(unit) {
		return fmt.Errorf("invalid unit name")
	}
	switch verb {
	case "restart", "stop", "start":
	default:
		return fmt.Errorf("unsupported action %q", verb)
	}
	return runEscalating(client, fmt.Sprintf("systemctl --no-ask-password %s %s", verb, quoteAlways(unit)), password)
}

// runEscalating runs cmd as the login user; on a permission refusal it
// retries as root through the cheapest route there is: already root, then
// passwordless sudo, then sudo with the given password.
func runEscalating(client *ssh.Client, cmd, password string) error {
	out, err := runOutput(client, cmd+" 2>&1")
	if err == nil {
		return nil
	}
	if !looksLikePermission(out) {
		return fmt.Errorf("%s", strings.TrimSpace(firstNonEmpty(strings.TrimSpace(out), err.Error())))
	}
	root, nopw, perr := CheckRootOrSudo(client)
	if perr != nil {
		return perr
	}
	switch {
	case root:
		return fmt.Errorf("%s", strings.TrimSpace(out))
	case nopw:
		if o, e := runOutput(client, "sudo -n "+cmd+" 2>&1"); e != nil {
			return fmt.Errorf("%s", strings.TrimSpace(firstNonEmpty(strings.TrimSpace(o), e.Error())))
		}
		return nil
	case password == "":
		return ErrSudoPassword
	}
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(password + "\n")
	var buf bytes.Buffer
	sess.Stdout, sess.Stderr = &buf, &buf
	if err := sess.Run("sudo -S -p '' " + cmd); err != nil {
		o := buf.String()
		if strings.Contains(o, "incorrect password") || strings.Contains(o, "Sorry, try again") {
			return errors.New("sudo: wrong password")
		}
		return fmt.Errorf("%s", strings.TrimSpace(firstNonEmpty(strings.TrimSpace(o), err.Error())))
	}
	return nil
}

func looksLikePermission(out string) bool {
	o := strings.ToLower(out)
	for _, s := range []string{"operation not permitted", "access denied", "permission denied", "interactive authentication required", "authentication is required"} {
		if strings.Contains(o, s) {
			return true
		}
	}
	return false
}

func runOutput(client *ssh.Client, cmd string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no live ssh client")
	}
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}
