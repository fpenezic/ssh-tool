package ssh

import (
	"os/exec"
	"runtime"
	"testing"
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
	if f.Updates != -1 || f.Security != -1 || f.Failed != -1 {
		t.Errorf("unknowns must stay -1: %+v", f)
	}
}
