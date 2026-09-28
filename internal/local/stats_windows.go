//go:build windows

package local

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	sshlayer "ssh-tool/internal/ssh"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// cpuSample is how long windowsStats watches the CPU. Windows has no load
// average, so busy share is measured as a delta between two readings of
// GetSystemTimes; 300ms is long enough to be stable, short enough for a
// 10s poll.
const cpuSample = 300 * time.Millisecond

// windowsStats reads the host for a PowerShell or cmd shell straight from
// the Win32 API: memory, fixed drives, CPU busy share, uptime, version.
// Users stays -1 (unknown) and the load fields stay 0 - the frontend shows
// CPUPct instead whenever it is set.
func windowsStats() (*sshlayer.ServerStats, error) {
	st := &sshlayer.ServerStats{MemUsedPct: -1, DiskUsedPct: -1, Users: -1, CPUPct: -1, FailedUnits: -1, NCPU: runtime.NumCPU()}

	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r != 0 && m.TotalPhys > 0 {
		st.MemTotalKB = int64(m.TotalPhys / 1024)
		st.MemAvailKB = int64(m.AvailPhys / 1024)
		st.MemUsedPct = (1 - float64(m.AvailPhys)/float64(m.TotalPhys)) * 100
		st.OK = true
	}

	sysDrive := os.Getenv("SystemDrive") // "C:"
	if mask, err := windows.GetLogicalDrives(); err == nil {
		for i := 0; i < 26; i++ {
			if mask&(1<<uint(i)) == 0 {
				continue
			}
			letter := string(rune('A'+i)) + ":"
			root, _ := windows.UTF16PtrFromString(letter + `\`)
			if windows.GetDriveType(root) != windows.DRIVE_FIXED {
				continue
			}
			var avail, total, free uint64
			if windows.GetDiskFreeSpaceEx(root, &avail, &total, &free) != nil || total == 0 {
				continue
			}
			used := total - free
			pct := float64(used) / float64(total) * 100
			st.Partitions = append(st.Partitions, sshlayer.DiskPart{
				Mount: letter, FS: volumeFS(root),
				SizeKB: int64(total / 1024), UsedKB: int64(used / 1024), AvailKB: int64(avail / 1024),
				UsedPct: pct, InodePct: -1,
			})
			if letter == sysDrive {
				st.DiskUsedPct = pct
			}
			st.OK = true
		}
	}

	if pct, ok := cpuBusy(); ok {
		st.CPUPct = pct
		st.OK = true
	}
	if r, _, _ := procGetTickCount64.Call(); r != 0 {
		st.UptimeSec = int64(r / 1000)
	}
	st.Hostname, _ = os.Hostname()
	if v := windows.RtlGetVersion(); v != nil {
		st.Kernel = fmt.Sprintf("Windows %d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	}
	if !st.OK {
		return nil, fmt.Errorf("no readable host stats")
	}
	return st, nil
}

func systemTimes() (idle, kernel, user uint64, ok bool) {
	var i, k, u windows.Filetime
	r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	if r == 0 {
		return 0, 0, 0, false
	}
	ft := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	return ft(i), ft(k), ft(u), true
}

// cpuBusy is the busy share of all cores over cpuSample. Kernel time
// includes idle time, so busy = (kernel + user - idle) / (kernel + user).
func cpuBusy() (float64, bool) {
	i1, k1, u1, ok := systemTimes()
	if !ok {
		return 0, false
	}
	time.Sleep(cpuSample)
	i2, k2, u2, ok := systemTimes()
	if !ok {
		return 0, false
	}
	total := (k2 - k1) + (u2 - u1)
	if total == 0 {
		return 0, false
	}
	return float64(total-(i2-i1)) / float64(total) * 100, true
}

func volumeFS(root *uint16) string {
	buf := make([]uint16, 32)
	if windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &buf[0], uint32(len(buf))) != nil {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// hideConsole keeps wsl.exe from flashing a console window over the GUI on
// every poll.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
