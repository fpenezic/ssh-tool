package ssh

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// The script runs on the machine the tests run on: the cheapest guard that
// every snippet is valid sh and the parser agrees with what it prints.
func TestFactsScriptRunsHere(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	cmd, err := BuildFactsCommand(FactKeys, "echo custom-ok")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	f := ParseFacts(string(out))
	if f.CPUCores <= 0 || f.MemKB <= 0 || f.OS == "" || f.Kernel == "" || f.UptimeSec <= 0 || len(f.Disks) == 0 {
		t.Fatalf("facts = %+v\nraw:\n%s", f, out)
	}
	if len(f.Disks) > 0 && f.Disks[0].UsedPct <= 0 {
		t.Errorf("disk use not read: %+v", f.Disks)
	}
	if f.LastPatch == 0 && f.Reboots30d == -1 {
		t.Logf("no package db / wtmp here")
	}
	if f.Custom != "custom-ok" {
		t.Errorf("custom = %q", f.Custom)
	}
	t.Logf("%+v", f)
}

func TestTidyCPUModel(t *testing.T) {
	for in, want := range map[string]string{
		" Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz": "Xeon Gold 6230",
		" AMD EPYC 7443 24-Core Processor":          "EPYC 7443 24-Core",
		"AMD Ryzen 9 7950X 16-Core Processor":       "Ryzen 9 7950X 16-Core",
	} {
		if got := tidyCPUModel(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestParseFactsMissingSections(t *testing.T) {
	f := ParseFacts(factMarker + "updates\n-\n-\n" + factMarker + "failed\n\n")
	if f.Updates != -1 || f.Security != -1 || f.Failed != -1 || f.Reboots30d != -1 {
		t.Errorf("unknowns must stay -1: %+v", f)
	}
	f = ParseFacts(factMarker + "failed\n-\n")
	if f.Failed != -1 {
		t.Errorf("unknowns must stay -1: %+v", f)
	}
}

func TestParseFactsMaintenance(t *testing.T) {
	out := factMarker + "kernelpending\n6.1.0-25-amd64\n6.1.0-26-amd64\n6.1.0-26-amd64\n" +
		factMarker + "lastpatch\n1759900000\n" +
		factMarker + "reboots\n2\n" +
		factMarker + "inodes\nFilesystem Inodes IUsed IFree IUse% Mounted on\n/dev/sda1 655360 600000 55360 92% /\n/dev/sdb1 - - - - /data\ntmpfs 1000 1 999 1% /run\n"
	f := ParseFacts(out)
	if f.Kernel != "6.1.0-25-amd64" || f.KernelLatest != "6.1.0-26-amd64" || f.KernelPending != "yes" {
		t.Errorf("kernel = %q %q %q", f.Kernel, f.KernelLatest, f.KernelPending)
	}
	if f.LastPatch != 1759900000 || f.Reboots30d != 2 {
		t.Errorf("lastpatch/reboots = %d %d", f.LastPatch, f.Reboots30d)
	}
	if len(f.Inodes) != 1 || f.Inodes[0] != (MountPct{Mount: "/", Pct: 92}) {
		t.Errorf("inodes = %+v", f.Inodes)
	}

	// Running the newest, and a container with nothing in /boot.
	if f := ParseFacts(factMarker + "kernelpending\n6.8.12-4-pve\n6.8.12-4-pve\n6.8.12-4-pve\n"); f.KernelPending != "no" {
		t.Errorf("current kernel = %q", f.KernelPending)
	}
	if f := ParseFacts(factMarker + "kernelpending\n6.8.12-4-pve\n"); f.KernelPending != "unknown" || f.Kernel != "6.8.12-4-pve" {
		t.Errorf("no /boot = %q %q", f.KernelPending, f.Kernel)
	}
}

func TestRunningFact(t *testing.T) {
	out := factMarker + "os\nDebian 12\n" + factMarker + "updates\n"
	if got := RunningFact(out); got != "updates" {
		t.Errorf("running = %q", got)
	}
	if got := RunningFact(""); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestParseFactsFailedAndNetworkDisks(t *testing.T) {
	out := factMarker + "failed\nok\nfoo.service\nbar.timer\n" +
		factMarker + "disks\nFilesystem 1024-blocks Used Available Capacity Mounted on\n" +
		"/dev/sda1 78000000 40000000 38000000 52% /\n" +
		"//nas.example.com/backup 10737418240 5000000000 5737418240 47% /mnt/backup\n" +
		"nfs.example.com:/export 1000000 10 999990 1% /mnt/nfs\n" +
		factMarker + "inodes\nFilesystem Inodes IUsed IFree IUse% Mounted on\n/dev/sda1 655360 60000 595360 10% /\n//nas.example.com/backup 0 0 0 0% /mnt/backup\n"
	f := ParseFacts(out)
	if f.Failed != 2 || len(f.FailedUnits) != 2 || f.FailedUnits[0] != "foo.service" {
		t.Errorf("failed = %d %v", f.Failed, f.FailedUnits)
	}
	if f2 := ParseFacts(factMarker + "failed\nok\n"); f2.Failed != 0 {
		t.Errorf("no failed units = %d", f2.Failed)
	}
	net := map[string]bool{}
	for _, d := range f.Disks {
		net[d.Mount] = d.Network
	}
	if net["/"] || !net["/mnt/backup"] || !net["/mnt/nfs"] || len(f.Disks) != 3 {
		t.Errorf("disks = %+v", f.Disks)
	}
	if len(f.Inodes) != 1 {
		t.Errorf("inodes = %+v", f.Inodes)
	}
}

func TestParsePatchTime(t *testing.T) {
	if parsePatchTime("1759900000") != 1759900000 {
		t.Error("epoch")
	}
	want := time.Date(2026, 10, 4, 2, 0, 0, 0, time.Local).Unix()
	for _, v := range []string{"2026-10-04 02:00", "2026-10-04  02:00"} {
		if got := parsePatchTime(v); got != want {
			t.Errorf("%q = %d, want %d", v, got, want)
		}
	}
	if parsePatchTime("-") != 0 {
		t.Error("unknown")
	}
}
