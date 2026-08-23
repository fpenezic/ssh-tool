//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS. x/sys/windows exposes
// GetProcessMemoryInfo only through psapi, which is not in the package, so the
// struct and the call are declared here.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var (
	modpsapi                 = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

// listProcesses walks the system process snapshot and reads each one's working
// set - the number Task Manager's "Memory" column shows.
//
// Processes we cannot open are still listed, with a zero working set: a
// truncated tree would silently under-report the total, which is the one
// mistake this endpoint exists to prevent. In practice our own children all
// open fine, since we created them.
func listProcesses() ([]procInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil, err
	}

	var out []procInfo
	for {
		p := procInfo{
			PID:  int(entry.ProcessID),
			PPID: int(entry.ParentProcessID),
			Name: windows.UTF16ToString(entry.ExeFile[:]),
		}
		p.WorkingSet = workingSet(entry.ProcessID)
		out = append(out, p)

		if err := windows.Process32Next(snap, &entry); err != nil {
			if err == syscall.ERROR_NO_MORE_FILES {
				break
			}
			return out, nil
		}
	}
	return out, nil
}

func workingSet(pid uint32) uint64 {
	// PROCESS_QUERY_LIMITED_INFORMATION is enough for the memory counters and
	// succeeds against processes a full QUERY_INFORMATION open would not.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)

	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	r, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB))
	if r == 0 {
		return 0
	}
	return uint64(pmc.WorkingSetSize)
}
