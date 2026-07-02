package ssh

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.5 }

func TestParseServerStats_Full(t *testing.T) {
	out := "0.42 0.55 0.60 1/234 5678\n" + statsSep + "\n" +
		"MemTotal:       16384000 kB\n" +
		"MemFree:         1000000 kB\n" +
		"MemAvailable:    8192000 kB\n" +
		"Buffers:          123456 kB\n" + statsSep + "\n" +
		"Filesystem     1024-blocks     Used Available Capacity Mounted on\n" +
		"/dev/sda1        41152000 27000000  12000000      68% /\n" + statsSep + "\n" +
		"root     pts/0\n# users=2\n"

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
	if !approx(s.DiskUsedPct, 68) {
		t.Errorf("disk used = %v, want 68", s.DiskUsedPct)
	}
	if s.Users != 2 {
		t.Errorf("users = %d, want 2", s.Users)
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
