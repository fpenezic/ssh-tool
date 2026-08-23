//go:build !windows && !android && !ios

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// listProcesses reads /proc. The endpoint exists for Windows (that is where
// the WebView2 processes are), but keeping a working Unix implementation means
// the tree walk and the formatting can be exercised on the dev machine instead
// of only ever running in production.
func listProcesses() ([]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []procInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p, ok := readProcStatus(pid)
		if !ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// readProcStatus pulls name, ppid and RSS out of /proc/<pid>/status. A process
// that exits mid-scan simply drops out, which is why a read error is not
// fatal here.
func readProcStatus(pid int) (procInfo, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return procInfo{}, false
	}
	p := procInfo{PID: pid}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch name {
		case "Name":
			p.Name = value
		case "PPid":
			if n, err := strconv.Atoi(value); err == nil {
				p.PPID = n
			}
		case "VmRSS":
			// "1234 kB"
			fields := strings.Fields(value)
			if len(fields) > 0 {
				if kb, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
					p.WorkingSet = kb * 1024
				}
			}
		}
	}
	return p, p.Name != ""
}
