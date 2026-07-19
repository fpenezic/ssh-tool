package ssh

import (
	"math"
	"strings"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.5 }

func TestParseServerStats_Full(t *testing.T) {
	// 8-section probe: load / meminfo(+swap) / df -Pk(all mounts) / who /
	// hostname / kernel / uptime / nproc. The df block deliberately mixes
	// real mounts (/ and /home) with pseudo/temp ones (tmpfs, /boot/efi, a
	// loop-mounted snap) to prove the filter.
	out := "0.42 0.55 0.60 1/234 5678\n" + statsSep + "\n" +
		"MemTotal:       16384000 kB\n" +
		"MemFree:         1000000 kB\n" +
		"MemAvailable:    8192000 kB\n" +
		"Buffers:          123456 kB\n" +
		"SwapTotal:       2000000 kB\n" +
		"SwapFree:        1500000 kB\n" + statsSep + "\n" +
		"Filesystem     1024-blocks     Used Available Capacity Mounted on\n" +
		"/dev/sda1        41152000 27000000  12000000      68% /\n" +
		"/dev/sdb1       100000000 40000000  60000000      40% /home\n" +
		"tmpfs             8192000        0   8192000       0% /run\n" +
		"tmpfs             8192000     1000   8191000       1% /dev/shm\n" +
		"/dev/loop3         131072   131072         0     100% /snap/core/1\n" +
		"/dev/sda2          523248     6284    516964       2% /boot/efi\n" + statsSep + "\n" +
		"root     admin\n# users=2\n" + statsSep + "\n" +
		"web01.example.com\n" + statsSep + "\n" +
		"6.1.0-18-amd64\n" + statsSep + "\n" +
		"123456.78 987654.32\n" + statsSep + "\n" +
		"8\n"

	s := parseServerStats(out)
	if !s.OK {
		t.Fatal("expected OK")
	}
	if !approx(s.Load1, 0.42) || !approx(s.Load5, 0.55) || !approx(s.Load15, 0.60) {
		t.Errorf("load = %v/%v/%v", s.Load1, s.Load5, s.Load15)
	}
	// used = (1 - 8192000/16384000) * 100 = 50%.
	if !approx(s.MemUsedPct, 50) {
		t.Errorf("mem used = %v, want ~50", s.MemUsedPct)
	}
	if s.MemTotalKB != 16384000 || s.MemAvailKB != 8192000 {
		t.Errorf("mem KB = %d/%d, want 16384000/8192000", s.MemTotalKB, s.MemAvailKB)
	}
	if s.SwapTotalKB != 2000000 || s.SwapFreeKB != 1500000 {
		t.Errorf("swap KB = %d/%d, want 2000000/1500000", s.SwapTotalKB, s.SwapFreeKB)
	}
	// Compact readout still tracks the root fs.
	if !approx(s.DiskUsedPct, 68) {
		t.Errorf("disk used = %v, want 68", s.DiskUsedPct)
	}
	// Only real mounts kept: / and /home. tmpfs, snap loop, /boot/efi dropped.
	if len(s.Partitions) != 2 {
		t.Fatalf("partitions = %d (%+v), want 2 (/ and /home)", len(s.Partitions), s.Partitions)
	}
	byMount := map[string]DiskPart{}
	for _, p := range s.Partitions {
		byMount[p.Mount] = p
	}
	root, ok := byMount["/"]
	if !ok {
		t.Fatal("missing / partition")
	}
	if root.SizeKB != 41152000 || root.UsedKB != 27000000 || root.AvailKB != 12000000 || !approx(root.UsedPct, 68) {
		t.Errorf("root part = %+v", root)
	}
	if _, ok := byMount["/home"]; !ok {
		t.Error("missing /home partition")
	}
	if _, ok := byMount["/boot/efi"]; ok {
		t.Error("/boot/efi should be filtered out")
	}
	for m := range byMount {
		if strings.HasPrefix(m, "/snap") || m == "/run" || m == "/dev/shm" {
			t.Errorf("pseudo mount %q not filtered", m)
		}
	}
	if s.Users != 2 {
		t.Errorf("users = %d, want 2", s.Users)
	}
	if len(s.UserNames) != 2 || s.UserNames[0] != "root" || s.UserNames[1] != "admin" {
		t.Errorf("user names = %v, want [root admin]", s.UserNames)
	}
	if s.Hostname != "web01.example.com" {
		t.Errorf("hostname = %q", s.Hostname)
	}
	if s.Kernel != "6.1.0-18-amd64" {
		t.Errorf("kernel = %q", s.Kernel)
	}
	if s.UptimeSec != 123456 {
		t.Errorf("uptime = %d, want 123456", s.UptimeSec)
	}
	if s.NCPU != 8 {
		t.Errorf("ncpu = %d, want 8", s.NCPU)
	}
}

func TestParseServerStats_Empty(t *testing.T) {
	// A host that answered nothing (network gear etc.) -> not OK, metrics
	// stay at the "unknown" sentinels.
	s := parseServerStats("\n" + statsSep + "\n" + statsSep + "\n" + statsSep + "\n")
	if s.OK {
		t.Fatal("expected not OK for empty probe")
	}
	if s.MemUsedPct != -1 || s.DiskUsedPct != -1 || s.Users != -1 {
		t.Errorf("unknown sentinels not preserved: %+v", s)
	}
}

func TestParseServerStats_PartialLoadOnly(t *testing.T) {
	// Only /proc/loadavg available (no meminfo/df/who) -> OK from load,
	// the rest unknown.
	s := parseServerStats("1.00 2.00 3.00 1/1 1\n" + statsSep + "\n" + statsSep + "\n" + statsSep + "\n")
	if !s.OK {
		t.Fatal("expected OK from loadavg")
	}
	if !approx(s.Load1, 1.0) {
		t.Errorf("load1 = %v", s.Load1)
	}
	if s.MemUsedPct != -1 || s.DiskUsedPct != -1 || s.Users != -1 {
		t.Errorf("expected unknown for missing sections: %+v", s)
	}
}
