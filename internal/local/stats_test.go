//go:build linux

package local

import "testing"

// The Linux probe runs on the machine the tests run on, so it must parse
// into real numbers there - the cheapest guard that StatsProbeCommand and
// the parser still agree.
func TestHostStatsLinux(t *testing.T) {
	s := &Session{Kind: "bash", Display: "bash"}
	st, err := s.HostStats()
	if err != nil {
		t.Fatal(err)
	}
	if !st.OK || st.MemUsedPct < 0 || st.NCPU <= 0 || len(st.Partitions) == 0 {
		t.Fatalf("stats = %+v", st)
	}
	if st.Shell != "bash" || st.CPUPct != -1 {
		t.Errorf("shell = %q cpu_pct = %v, want bash / -1", st.Shell, st.CPUPct)
	}
}
